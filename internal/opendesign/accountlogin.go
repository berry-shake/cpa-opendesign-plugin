package opendesign

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/vela"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Signing an OpenDesign account in on a key card, the way `vela login` does:
// the plugin starts a device authorization, the page shows the activation
// link and code, and the plugin polls until the user confirms. The issued
// control key goes into the credential's auth file and never to the page.
// Every call goes out through the credential's egress.

const (
	// The routes, relative to the plugin's management base.
	accountLoginPath = "/account/login"
	accountPath      = "/account"

	// loginKeep is how long a finished sign-in stays readable.
	loginKeep = 10 * time.Minute
	// loginMaxWait caps a sign-in whose expiry the API did not say.
	loginMaxWait = 15 * time.Minute
)

// Sign-in states the page sees.
const (
	loginPending   = "pending"
	loginApproved  = "approved"
	loginDenied    = "denied"
	loginExpired   = "expired"
	loginCancelled = "cancelled"
	loginFailed    = "failed"
)

type loginSession struct {
	id        string
	authIndex string

	deviceID      string
	deviceSecret  string
	userCode      string
	activationURL string
	interval      time.Duration
	expiresAt     time.Time
	client        vela.Client
	cancel        context.CancelFunc

	mu    sync.Mutex
	state string
	// saving is set once the sign-in was approved and its keys are being
	// stored; it can no longer be cancelled.
	saving     bool
	message    string
	email      string
	finishedAt time.Time
}

type loginView struct {
	ID            string `json:"id"`
	AuthIndex     string `json:"auth_index"`
	State         string `json:"state"`
	UserCode      string `json:"user_code,omitempty"`
	ActivationURL string `json:"activation_url,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Email         string `json:"email,omitempty"`
	Message       string `json:"message,omitempty"`
}

func (l *loginSession) view() loginView {
	l.mu.Lock()
	defer l.mu.Unlock()
	view := loginView{ID: l.id, AuthIndex: l.authIndex, State: l.state, Email: l.email, Message: l.message}
	if l.state == loginPending {
		view.UserCode, view.ActivationURL = l.userCode, l.activationURL
		view.ExpiresAt = l.expiresAt.UTC().Format(time.RFC3339)
	}
	return view
}

// finish ends a pending sign-in; it reports false when it had already ended.
func (l *loginSession) finish(state, message, email string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != loginPending {
		return false
	}
	l.state, l.message, l.email, l.finishedAt = state, message, email, now
	return true
}

// beginSave marks an approved sign-in as being stored; false when it was
// cancelled first.
func (l *loginSession) beginSave() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != loginPending {
		return false
	}
	l.saving = true
	return true
}

// cancellable reports whether a sign-in may still be abandoned.
func (l *loginSession) cancellable() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == loginPending && !l.saving
}

func (l *loginSession) note(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == loginPending {
		l.message = message
	}
}

type loginRegistry struct {
	mu    sync.Mutex
	items map[string]*loginSession
}

func newLoginRegistry() *loginRegistry {
	return &loginRegistry{items: make(map[string]*loginSession)}
}

func (r *loginRegistry) get(id string) (*loginSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.items[strings.TrimSpace(id)]
	return l, ok
}

// add registers a sign-in and returns the pending ones of the same
// credential, which the new one replaces. Finished sign-ins older than
// loginKeep are dropped.
func (r *loginRegistry) add(l *loginSession, now time.Time) []*loginSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	var replaced []*loginSession
	for id, other := range r.items {
		other.mu.Lock()
		state, finished := other.state, other.finishedAt
		other.mu.Unlock()
		switch {
		case state == loginPending && other.authIndex == l.authIndex:
			replaced = append(replaced, other)
		case state != loginPending && now.Sub(finished) > loginKeep:
			delete(r.items, id)
		}
	}
	r.items[l.id] = l
	return replaced
}

func newLoginID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// velaClient is the account API client for a credential: its egress, and
// the API origin its login reported (the default before signing in; see
// accountAPIURL).
func (a *Adapter) velaClient(proxyURL, apiURL string) (vela.Client, error) {
	client, errProxy := a.host.Client(proxyURL)
	if errProxy != nil {
		return vela.Client{}, errProxy
	}
	return withAPIURL(vela.Client{HTTP: client}, a.accountAPIURL(apiURL)), nil
}

// accountAPIURL resolves the account API origin of keys issued with apiURL
// (a token's apiUrl, saved as account_api_url): that origin, or else the
// adapter's default (velaAPIURL; vela's production origin when that is
// empty too). Every account API call goes through it, so keys are read and
// revoked where they were issued.
func (a *Adapter) accountAPIURL(apiURL string) string {
	if strings.TrimSpace(apiURL) != "" {
		return apiURL
	}
	return a.velaAPIURL
}

// withAPIURL is client talking to another account API origin through the
// same egress.
func withAPIURL(client vela.Client, apiURL string) vela.Client {
	client.APIURL = apiURL
	return client
}

// startAccountLogin starts a device authorization for a credential
// ({"auth_index"}). A sign-in already waiting on the same card is cancelled.
func (a *Adapter) startAccountLogin(body []byte) pluginapi.ManagementResponse {
	var input struct {
		AuthIndex string `json:"auth_index"`
	}
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil || strings.TrimSpace(input.AuthIndex) == "" {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_json", Message: `send {"auth_index"}`})
	}
	entries, errList := a.host.AuthFiles()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	entry, found := findAuth(entries, input.AuthIndex)
	if !found {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no OpenDesign credential matches auth_index"})
	}
	if entry.ReadError != "" {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "read_failed", Message: entry.ReadError})
	}
	if credential.SaveName(entry.Name, entry.Path) == "" {
		return jsonResponse(http.StatusConflict, errorBody{Error: "not_file_backed", Message: "this credential has no auth file the plugin can save the login to"})
	}
	client, errClient := a.velaClient(entry.Credential.ProxyURL, "")
	if errClient != nil {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_proxy", Message: errClient.Error()})
	}
	host, errHost := a.hostname()
	if errHost != nil {
		return jsonResponse(http.StatusInternalServerError, errorBody{Error: "hostname_unavailable", Message: "resolve hostname: " + errHost.Error()})
	}
	ctx, cancel := context.WithTimeout(a.host.Env().Context, provider.AccountCallTimeout)
	device, errStart := client.StartDeviceAuthorization(ctx, host)
	cancel()
	if errStart != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "login_start_failed", Message: "start device authorization: " + errStart.Error()})
	}

	now := a.now()
	expires := device.ExpiresAt
	if expires.IsZero() || expires.Before(now) || expires.Sub(now) > loginMaxWait {
		expires = now.Add(loginMaxWait)
	}
	loginCtx, loginCancel := context.WithCancel(a.host.Env().Context)
	login := &loginSession{
		id:            newLoginID(),
		authIndex:     entry.AuthIndex,
		deviceID:      device.DeviceID,
		deviceSecret:  device.DeviceSecret,
		userCode:      device.UserCode,
		activationURL: device.ActivationURL,
		interval:      time.Duration(device.PollIntervalSeconds) * time.Second,
		expiresAt:     expires,
		client:        client,
		cancel:        loginCancel,
		state:         loginPending,
	}
	for _, old := range a.logins.add(login, now) {
		a.abandonLogin(old, loginCancelled, "replaced by a new sign-in")
	}
	a.logf("info", "opendesign: account sign-in started", map[string]any{"auth_index": entry.AuthIndex})
	go a.pollLogin(loginCtx, login)
	return jsonResponse(http.StatusCreated, login.view())
}

// pollLogin waits for the user to confirm the sign-in, polling at the
// interval the API asked for, until it is approved, refused or expires.
func (a *Adapter) pollLogin(ctx context.Context, login *loginSession) {
	defer login.cancel()
	timer := time.NewTimer(login.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if !a.now().Before(login.expiresAt) {
			a.abandonLogin(login, loginExpired, "")
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, provider.AccountCallTimeout)
		token, errPoll := login.client.PollToken(callCtx, login.deviceID, login.deviceSecret)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if errPoll != nil {
			var apiErr *vela.Error
			if errors.As(errPoll, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != http.StatusTooManyRequests {
				login.finish(loginFailed, "poll device authorization token: "+errPoll.Error(), "", a.now())
				return
			}
			// A network hiccup or a 5xx: keep waiting until the sign-in expires.
			login.note("poll device authorization token: " + errPoll.Error())
			timer.Reset(login.interval)
			continue
		}
		switch token.Status {
		case vela.StatusPending:
			login.note("")
			timer.Reset(login.interval)
			continue
		case vela.StatusApproved:
			if login.beginSave() {
				// Storing and confirming the keys runs to the end even if the
				// page cancels meanwhile.
				a.saveApprovedLogin(a.host.Env().Context, login, token)
			} else if token.Validate() == nil {
				// Cancelled just as it was approved: drop the issued keys.
				revokeCtx, cancel := context.WithTimeout(a.host.Env().Context, provider.AccountCallTimeout)
				_ = withAPIURL(login.client, a.accountAPIURL(token.APIURL)).Logout(revokeCtx, token.ControlKey, token.ControlKeyID, token.User.ID)
				cancel()
			}
		case vela.StatusCancelled:
			login.finish(loginDenied, "", "", a.now())
		case vela.StatusExpired:
			login.finish(loginExpired, "", "", a.now())
		case vela.StatusConsumed:
			login.finish(loginFailed, "this sign-in was already used; start a new one", "", a.now())
		default:
			login.finish(loginFailed, "device authorization returned unsupported status "+token.Status, "", a.now())
		}
		return
	}
}

// saveApprovedLogin stores the issued keys in the credential's auth file,
// then confirms the sign-in to the API (completionReceipt). Keys that
// cannot be saved or confirmed are revoked again.
func (a *Adapter) saveApprovedLogin(ctx context.Context, login *loginSession, token vela.Token) {
	if errValid := token.Validate(); errValid != nil {
		login.finish(loginFailed, errValid.Error(), "", a.now())
		return
	}
	client := withAPIURL(login.client, a.accountAPIURL(token.APIURL))
	revoke := func() {
		revokeCtx, cancel := context.WithTimeout(a.host.Env().Context, provider.AccountCallTimeout)
		defer cancel()
		if errRevoke := client.Logout(revokeCtx, token.ControlKey, token.ControlKeyID, token.User.ID); errRevoke != nil {
			a.logf("warn", "opendesign: cannot revoke unsaved account keys", map[string]any{"auth_index": login.authIndex, "error": errRevoke.Error()})
		}
	}

	// The workspace whose coding plan the card shows.
	var workspace vela.Workspace
	callCtx, cancel := context.WithTimeout(ctx, provider.AccountCallTimeout)
	items, errList := client.Workspaces(callCtx, token.ControlKey)
	cancel()
	if errList != nil {
		a.logf("warn", "opendesign: cannot list the account's workspaces", map[string]any{"auth_index": login.authIndex, "error": errList.Error()})
	} else if picked, ok := vela.PickWorkspace(items); ok {
		workspace = picked
	}

	fields, name, errRead := a.host.ReadAuthFile(login.authIndex)
	if errRead != nil {
		revoke()
		login.finish(loginFailed, "credentials were issued but could not be saved: "+errRead.Error(), "", a.now())
		return
	}
	previous := AccountFromMap(fields)
	before := cloneFields(fields)
	for _, key := range AccountKeys {
		delete(fields, key)
	}
	setOrDelete(fields, KeyAccountControlKey, token.ControlKey)
	setOrDelete(fields, KeyAccountControlKeyID, token.ControlKeyID)
	setOrDelete(fields, KeyAccountAPIURL, token.APIURL)
	setOrDelete(fields, KeyAccountUserID, token.User.ID)
	setOrDelete(fields, KeyAccountEmail, token.User.Email)
	setOrDelete(fields, KeyAccountName, token.User.Name)
	setOrDelete(fields, KeyAccountWorkspaceID, workspace.WorkspaceID)
	setOrDelete(fields, KeyAccountWorkspaceName, workspace.WorkspaceName)
	setOrDelete(fields, KeyAccountLoggedInAt, a.now().UTC().Format(time.RFC3339))
	if errSave := a.host.SaveAuthFile(name, fields); errSave != nil {
		revoke()
		login.finish(loginFailed, "credentials were issued but could not be saved: "+errSave.Error(), "", a.now())
		return
	}

	callCtx, cancel = context.WithTimeout(ctx, provider.AccountCallTimeout)
	errComplete := client.CompleteDeviceAuthorization(callCtx, login.deviceID, login.deviceSecret)
	cancel()
	if errComplete != nil {
		if errRestore := a.host.SaveAuthFile(name, before); errRestore != nil {
			a.logf("warn", "opendesign: cannot restore the auth file after a failed sign-in", map[string]any{"auth": name, "error": errRestore.Error()})
		}
		revoke()
		login.finish(loginFailed, "confirm saved credentials: "+errComplete.Error(), "", a.now())
		return
	}
	a.accounts.forget(login.authIndex)
	login.finish(loginApproved, "", token.User.Email, a.now())
	a.logf("info", "opendesign: account signed in", map[string]any{"auth_index": login.authIndex, "workspace": workspace.WorkspaceID != ""})

	// A sign-in that replaced an older one revokes the older keys.
	if previous.LoggedIn() && previous.ControlKey != token.ControlKey {
		old := withAPIURL(login.client, a.accountAPIURL(previous.APIURL))
		revokeCtx, cancel := context.WithTimeout(a.host.Env().Context, provider.AccountCallTimeout)
		defer cancel()
		if errRevoke := old.Logout(revokeCtx, previous.ControlKey, previous.ControlKeyID, previous.UserID); errRevoke != nil && !vela.Unauthorized(errRevoke) {
			a.logf("warn", "opendesign: cannot revoke the replaced account keys", map[string]any{"auth_index": login.authIndex, "error": errRevoke.Error()})
		}
	}
}

// abandonLogin ends a pending sign-in and tells the API to drop it.
func (a *Adapter) abandonLogin(login *loginSession, state, message string) {
	if !login.cancellable() || !login.finish(state, message, "", a.now()) {
		return
	}
	login.cancel()
	go func() {
		ctx, cancel := context.WithTimeout(a.host.Env().Context, provider.AccountCallTimeout)
		defer cancel()
		_ = login.client.CancelDeviceAuthorization(ctx, login.deviceID, login.deviceSecret)
	}()
}

func (a *Adapter) accountLoginStatus(id string) pluginapi.ManagementResponse {
	login, ok := a.logins.get(id)
	if !ok {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "login_not_found", Message: "no sign-in has this id; start a new one"})
	}
	return jsonResponse(http.StatusOK, login.view())
}

func (a *Adapter) cancelAccountLogin(id string) pluginapi.ManagementResponse {
	login, ok := a.logins.get(id)
	if !ok {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "login_not_found", Message: "no sign-in has this id"})
	}
	a.abandonLogin(login, loginCancelled, "")
	return jsonResponse(http.StatusOK, login.view())
}

type logoutResult struct {
	OK          bool   `json:"ok"`
	Revoked     bool   `json:"revoked"`
	RevokeError string `json:"revoke_error,omitempty"`
}

// logoutAccount signs the account out of a credential (?auth_index=): the
// control key is revoked, as `vela logout` does, and removed from the auth
// file. It is removed even when the API cannot be reached; the account's
// device list on the website can still revoke it.
func (a *Adapter) logoutAccount(authIndex string) pluginapi.ManagementResponse {
	fields, name, errRead := a.host.ReadAuthFile(strings.TrimSpace(authIndex))
	if errRead != nil || !credential.IsProvider(fields, ProviderID) {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no OpenDesign credential matches auth_index"})
	}
	account := AccountFromMap(fields)
	if !account.LoggedIn() {
		return jsonResponse(http.StatusConflict, errorBody{Error: "not_signed_in", Message: "no OpenDesign account is signed in on this credential"})
	}
	result := logoutResult{OK: true}
	client, errClient := a.velaClient(credential.FromMap(fields).ProxyURL, account.APIURL)
	if errClient == nil {
		ctx, cancel := context.WithTimeout(a.host.Env().Context, provider.AccountCallTimeout)
		errClient = client.Logout(ctx, account.ControlKey, account.ControlKeyID, account.UserID)
		cancel()
	}
	switch {
	case errClient == nil:
		result.Revoked = true
	case vela.Unauthorized(errClient):
		// Already revoked or expired.
		result.Revoked = true
	default:
		result.RevokeError = errClient.Error()
	}
	for _, key := range AccountKeys {
		delete(fields, key)
	}
	if errSave := a.host.SaveAuthFile(name, fields); errSave != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "save_failed", Message: errSave.Error()})
	}
	a.accounts.forget(authIndex)
	a.logf("info", "opendesign: account signed out", map[string]any{"auth_index": authIndex, "revoked": result.Revoked})
	return jsonResponse(http.StatusOK, result)
}

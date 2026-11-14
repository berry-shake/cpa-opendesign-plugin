package opendesign

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/berry-shake/cpa-provider-core/core"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// fakeHost keeps auth files in memory and answers the host callbacks the
// account sign-in uses.
type fakeHost struct {
	mu    sync.Mutex
	files map[string]map[string]any // auth index -> fields
	names map[string]string         // auth index -> file name
	saves int
}

func (h *fakeHost) call(method string, request []byte) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ok := func(v any) ([]byte, error) {
		raw, _ := json.Marshal(v)
		return json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
	}
	switch method {
	case pluginabi.MethodHostAuthList:
		var files []pluginapi.HostAuthFileEntry
		for index, name := range h.names {
			files = append(files, pluginapi.HostAuthFileEntry{AuthIndex: index, ID: name, Name: name, Provider: ProviderID, Type: ProviderID})
		}
		return ok(map[string]any{"files": files})
	case pluginabi.MethodHostAuthGet:
		var req pluginapi.HostAuthGetRequest
		_ = json.Unmarshal(request, &req)
		raw, _ := json.Marshal(h.files[req.AuthIndex])
		return ok(pluginapi.HostAuthGetResponse{AuthIndex: req.AuthIndex, Name: h.names[req.AuthIndex], JSON: raw})
	case pluginabi.MethodHostAuthSave:
		var req pluginapi.HostAuthSaveRequest
		_ = json.Unmarshal(request, &req)
		for index, name := range h.names {
			if name == req.Name {
				var fields map[string]any
				_ = json.Unmarshal(req.JSON, &fields)
				h.files[index] = fields
				h.saves++
			}
		}
		return ok(struct{}{})
	}
	return ok(struct{}{})
}

// testService is the plugin service with its adapter at hand.
type testService struct {
	*core.Service
	*Adapter
	// registration is the plugin.register answer.
	registration []byte
}

// testDeadOrigin is an address nothing answers on. The test services start
// out pointed at it for both the inference and the account API, so no test
// reaches OpenDesign's production origins by accident.
const testDeadOrigin = "http://127.0.0.1:9"

// newTestService registers the plugin with the request log in a temporary
// directory and without the catalog refresh loop.
func newTestService(t *testing.T, host *fakeHost) *testService {
	t.Helper()
	adapter := New()
	adapter.velaAPIURL = testDeadOrigin
	adapter.hostname = func() (string, error) { return "cpa-test-host", nil }
	svc := &testService{Service: core.NewWithOptions(adapter, "test", host.call, core.Options{ManualRefresh: true}), Adapter: adapter}
	register, _ := json.Marshal(map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"config_yaml":    []byte("base-url: " + testDeadOrigin + "\ndata-dir: " + t.TempDir() + "\n"),
	})
	svc.registration = svc.Call(pluginabi.MethodPluginRegister, register)
	return svc
}

// status is the management page's status view.
func (s *testService) status() json.RawMessage {
	request, _ := json.Marshal(map[string]any{"method": http.MethodGet, "path": "/v0/management/plugins/" + ProviderID + "/status"})
	var env pluginabi.Envelope
	_ = json.Unmarshal(s.Call(pluginabi.MethodManagementHandle, request), &env)
	var resp pluginapi.ManagementResponse
	_ = json.Unmarshal(env.Result, &resp)
	return resp.Body
}

func (h *fakeHost) fields(index string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]any, len(h.files[index]))
	for k, v := range h.files[index] {
		out[k] = v
	}
	return out
}

// fakeAccountAPI is the account API: the token stays pending for the first
// poll, then is approved. blocked names paths Cloudflare answers with its
// HTML 403 page instead of the API.
type fakeAccountAPI struct {
	mu        sync.Mutex
	polls     int
	completed bool
	revoked   []string
	agents    []string
	blocked   map[string]bool
}

const cloudflareBlock = "<!DOCTYPE html>\n<html><head><title>Attention Required! | Cloudflare</title></head><body>Sorry, you have been blocked</body></html>"

func (a *fakeAccountAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.agents = append(a.agents, r.Header.Get("User-Agent"))
		if a.blocked[r.URL.Path] {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, cloudflareBlock)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/cli/device-authorizations":
			if !strings.Contains(string(body), `"host":"cpa-test-host"`) {
				t.Errorf("start body = %s", body)
			}
			_, _ = io.WriteString(w, `{"deviceId":"dev-1","userCode":"WXYZ-1234","deviceSecret":"secret-1","activationUrl":"https://open-design.ai/cli/activate?deviceId=dev-1","pollIntervalSeconds":1,"status":"pending","expiresAt":"`+time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339)+`"}`)
		case "POST /api/v1/cli/device-authorizations/dev-1/token":
			a.polls++
			if a.polls == 1 {
				_, _ = io.WriteString(w, `{"status":"pending"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"approved","controlKey":"ck-secret","controlKeyId":"ck-1","runtimeKey":"rk-secret","user":{"id":"user-1","email":"me@example.com","name":"Me"},"apiUrl":"","linkUrl":""}`)
		case "POST /api/v1/cli/device-authorizations/dev-1/complete":
			a.completed = true
			_, _ = io.WriteString(w, `{"status":"completed"}`)
		case "GET /api/v1/workspaces":
			_, _ = io.WriteString(w, `{"items":[{"workspaceId":"ws-team","workspaceName":"Team","workspaceType":"team","workspaceMemberId":"m1","role":"owner","memberStatus":"active","lifecycleState":"active"},{"workspaceId":"ws-me","workspaceName":"Personal","workspaceType":"personal","workspaceMemberId":"m2","role":"owner","memberStatus":"active","lifecycleState":"active"}]}`)
		case "GET /api/v1/workspaces/ws-me/billing/funding-check":
			if r.Header.Get("Authorization") != "Bearer ck-secret" || r.Header.Get("X-Vela-Workspace-Id") != "ws-me" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"schemaVersion":1,"workspaceId":"ws-me","balanceUsd":"1.25","funding":"coding_plan","codingPlan":{"eligible":true,"tier":"go","windows":[{"policyId":"p5","durationSeconds":18000,"resetMode":"activity_triggered","usedCredits":"250","limitCredits":"1000","remainingCredits":"750","resetsAt":"2026-11-13T20:00:00Z"},{"policyId":"p7","durationSeconds":604800,"resetMode":"anchored_recurring","usedCredits":"4000","limitCredits":"5000","remainingCredits":"1000"}]}}`)
		case "GET /api/v1/workspaces/ws-me/billing/snapshot":
			if r.Header.Get("Authorization") != "Bearer ck-secret" || r.Header.Get("X-Vela-Workspace-Id") != "ws-me" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"schemaVersion":1,"workspaceId":"ws-me","plan":{"tier":"go","interval":"month","currentPeriodStart":"2026-11-13T16:06:05Z","currentPeriodEnd":"2026-12-13T16:06:05Z","cancelAtPeriodEnd":false,"status":"active"},"wallet":{"balanceUsd":"1.25"},"designPlanWindows":[]}`)
		case "POST /api/v1/cli/logout":
			a.revoked = append(a.revoked, string(body))
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestAccountSignInSavesKeysAndShowsWindows(t *testing.T) {
	api := &fakeAccountAPI{}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	host := &fakeHost{
		files: map[string]map[string]any{"idx-1": {"type": ProviderID, "api_key": "sk-amr-test", "proxy_url": "direct", "label": "mine"}},
		names: map[string]string{"idx-1": "opendesign-test.json"},
	}
	svc := newTestService(t, host)
	defer svc.Close()
	svc.velaAPIURL = srv.URL

	resp := svc.startAccountLogin([]byte(`{"auth_index":"idx-1"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start = %d %s", resp.StatusCode, resp.Body)
	}
	var started loginView
	_ = json.Unmarshal(resp.Body, &started)
	if started.State != loginPending || started.UserCode != "WXYZ-1234" || !strings.HasPrefix(started.ActivationURL, "https://") {
		t.Fatalf("started = %+v", started)
	}
	if strings.Contains(string(resp.Body), "secret-1") {
		t.Fatal("the device secret must not reach the page")
	}

	deadline := time.Now().Add(10 * time.Second)
	var view loginView
	for time.Now().Before(deadline) {
		_ = json.Unmarshal(svc.accountLoginStatus(started.ID).Body, &view)
		if view.State != loginPending {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if view.State != loginApproved || view.Email != "me@example.com" {
		t.Fatalf("sign-in ended as %+v", view)
	}
	fields := host.fields("idx-1")
	if fields["account_control_key"] != "ck-secret" || fields["account_workspace_id"] != "ws-me" || fields["api_key"] != "sk-amr-test" || fields["label"] != "mine" {
		t.Fatalf("saved fields = %v", fields)
	}
	if _, stored := fields["runtime_key"]; stored {
		t.Fatal("the runtime key must not be stored")
	}
	api.mu.Lock()
	completed := api.completed
	api.mu.Unlock()
	if !completed {
		t.Fatal("the sign-in was not confirmed with /complete")
	}

	// The card's figures carry the plan windows; the status view only names
	// the account.
	quota := svc.credentialQuota("idx-1", true)
	var usage usageView
	_ = json.Unmarshal(quota.Body, &usage)
	if usage.Account == nil || usage.Account.Error != "" || usage.Account.Tier != "go" || len(usage.Account.Windows) != 2 {
		t.Fatalf("account usage = %s", quota.Body)
	}
	if w := usage.Account.Windows[0]; w.Kind != "5h" || w.Remaining != 750 || w.Limit != 1000 {
		t.Fatalf("5h window = %+v", w)
	}
	if usage.Account.Windows[1].Kind != "7d" || usage.Account.PeriodEnd != "2026-12-13T16:06:05Z" || usage.Account.SubscriptionStatus != "active" {
		t.Fatalf("account usage = %s", quota.Body)
	}
	status := svc.status()
	if strings.Contains(string(status), "ck-secret") || strings.Contains(string(quota.Body), "ck-secret") {
		t.Fatal("the control key must not reach the page")
	}
	if !strings.Contains(string(status), `"email":"me@example.com"`) {
		t.Fatalf("status lacks the account: %s", status)
	}

	// Signing out revokes the key and removes the account fields.
	out := svc.logoutAccount("idx-1")
	if out.StatusCode != http.StatusOK || !strings.Contains(string(out.Body), `"revoked":true`) {
		t.Fatalf("logout = %d %s", out.StatusCode, out.Body)
	}
	if _, still := host.fields("idx-1")["account_control_key"]; still {
		t.Fatal("logout left the control key in the auth file")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.revoked) != 1 || !strings.Contains(api.revoked[0], `"controlKeyId":"ck-1"`) || !strings.Contains(api.revoked[0], `"expectedAppUserId":"user-1"`) {
		t.Fatalf("revocations = %v", api.revoked)
	}
	for _, agent := range api.agents {
		if !strings.HasPrefix(agent, "Go-http-client/") {
			t.Fatalf("User-Agent %q; the account API must see the one vela sends", agent)
		}
	}
}

func TestAccountSignInCancel(t *testing.T) {
	api := &fakeAccountAPI{}
	var cancelled sync.WaitGroup
	cancelled.Add(1)
	mux := http.NewServeMux()
	mux.Handle("/", api.handler(t))
	mux.HandleFunc("/api/v1/cli/device-authorizations/dev-1/cancel", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"deviceSecret":"secret-1"`) {
			t.Errorf("cancel body = %s", body)
		}
		cancelled.Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := &fakeHost{
		files: map[string]map[string]any{"idx-1": {"type": ProviderID, "api_key": "sk-amr-test", "proxy_url": "direct"}},
		names: map[string]string{"idx-1": "opendesign-test.json"},
	}
	svc := newTestService(t, host)
	defer svc.Close()
	svc.velaAPIURL = srv.URL

	var started loginView
	_ = json.Unmarshal(svc.startAccountLogin([]byte(`{"auth_index":"idx-1"}`)).Body, &started)
	var view loginView
	_ = json.Unmarshal(svc.cancelAccountLogin(started.ID).Body, &view)
	if view.State != loginCancelled {
		t.Fatalf("after cancel: %+v", view)
	}
	done := make(chan struct{})
	go func() { cancelled.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the API was not told to cancel")
	}
	if _, saved := host.fields("idx-1")["account_control_key"]; saved {
		t.Fatal("a cancelled sign-in saved keys")
	}
}

// signedIn is a service whose card idx-1 already has an account signed in.
func signedIn(t *testing.T, api *fakeAccountAPI) *testService {
	t.Helper()
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)
	host := &fakeHost{
		files: map[string]map[string]any{"idx-1": {
			"type": ProviderID, "api_key": "sk-amr-test", "proxy_url": "direct",
			"account_control_key": "ck-secret", "account_control_key_id": "ck-1", "account_user_id": "user-1",
			"account_email": "me@example.com", "account_workspace_id": "ws-me", "account_workspace_name": "Personal",
		}},
		names: map[string]string{"idx-1": "opendesign-test.json"},
	}
	svc := newTestService(t, host)
	t.Cleanup(svc.Close)
	svc.velaAPIURL = srv.URL
	return svc
}

func signedInAccount(t *testing.T, svc *testService) *accountUsageView {
	t.Helper()
	var usage usageView
	quota := svc.credentialQuota("idx-1", true)
	_ = json.Unmarshal(quota.Body, &usage)
	if usage.Account == nil {
		t.Fatalf("no account in %s", quota.Body)
	}
	return usage.Account
}

// Cloudflare refusing an optional read leaves the windows; it is not a
// sign-in that expired.
func TestAccountBlockedSnapshotKeepsWindows(t *testing.T) {
	svc := signedIn(t, &fakeAccountAPI{blocked: map[string]bool{"/api/v1/workspaces/ws-me/billing/snapshot": true}})
	a := signedInAccount(t, svc)
	if a.Expired || a.Error != "" || len(a.Windows) != 2 || a.PeriodEnd != "" {
		t.Fatalf("account = %+v", a)
	}
}

// Cloudflare refusing the windows is reported as a block by name, and the
// card does not ask to sign in again.
func TestAccountBlockedWindowsIsNotExpired(t *testing.T) {
	svc := signedIn(t, &fakeAccountAPI{blocked: map[string]bool{"/api/v1/workspaces/ws-me/billing/funding-check": true}})
	a := signedInAccount(t, svc)
	if a.Expired || !strings.Contains(a.Error, "Attention Required! | Cloudflare") || strings.Contains(a.Error, "<html") {
		t.Fatalf("account = %+v", a)
	}
}

// The API rejecting the control key asks to sign in again and keeps the
// API's reason.
func TestAccountRejectedKeyIsExpired(t *testing.T) {
	api := &fakeAccountAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_control_key","message":"control key revoked"}}`)
	}))
	defer srv.Close()
	svc := signedIn(t, api)
	svc.velaAPIURL = srv.URL
	a := signedInAccount(t, svc)
	if !a.Expired || !strings.Contains(a.Error, "control key revoked") {
		t.Fatalf("account = %+v", a)
	}
}

// CPA's auth file page reads the windows through quota.fetch.
func TestQuotaProviderReportsWindows(t *testing.T) {
	svc := signedIn(t, &fakeAccountAPI{})
	var reg struct {
		Capabilities struct {
			QuotaProvider bool `json:"quota_provider"`
		} `json:"capabilities"`
	}
	var env pluginabi.Envelope
	_ = json.Unmarshal(svc.registration, &env)
	_ = json.Unmarshal(env.Result, &reg)
	if !reg.Capabilities.QuotaProvider {
		t.Fatal("the plugin must register as a quota provider")
	}
	_ = json.Unmarshal(svc.Call(pluginabi.MethodQuotaDescribe, []byte(`{}`)), &env)
	if !env.OK || !strings.Contains(string(env.Result), `"supported_providers":["opendesign"]`) {
		t.Fatalf("describe = %+v %s", env.Error, env.Result)
	}

	raw := svc.Call(pluginabi.MethodQuotaFetch, []byte(`{"auth_index":"idx-1","auth_id":"opendesign-test.json","provider":"opendesign","host_callback_id":"cb-1"}`))
	env = pluginabi.Envelope{}
	_ = json.Unmarshal(raw, &env)
	if !env.OK {
		t.Fatalf("fetch = %s", raw)
	}
	var quota pluginapi.QuotaFetchResponse
	_ = json.Unmarshal(env.Result, &quota)
	if quota.Subscription == nil || quota.Subscription.Plan != "Go (Monthly)" {
		t.Fatalf("subscription = %s", env.Result)
	}
	if len(quota.Summary) != 1 || quota.Summary[0].Value != 1.25 || quota.Summary[0].Currency != "USD" {
		t.Fatalf("summary = %s", env.Result)
	}
	if len(quota.Groups) != 1 || len(quota.Groups[0].Buckets) != 2 {
		t.Fatalf("groups = %s", env.Result)
	}
	five, seven := quota.Groups[0].Buckets[0], quota.Groups[0].Buckets[1]
	if five.Window != "5h" || five.RemainingFraction != 0.75 || five.ResetTime != "2026-11-13T20:00:00Z" || seven.Window != "7d" || seven.RemainingFraction != 0.2 {
		t.Fatalf("buckets = %s", env.Result)
	}
	if strings.Contains(string(raw), "ck-secret") {
		t.Fatal("the control key must not reach CPA's quota response")
	}
}

// A key with no account signed in, or whose login the API rejects, says why
// instead of showing empty windows.
func TestQuotaProviderErrors(t *testing.T) {
	host := &fakeHost{
		files: map[string]map[string]any{"idx-2": {"type": ProviderID, "api_key": "sk-amr-test", "proxy_url": "direct"}},
		names: map[string]string{"idx-2": "opendesign-plain.json"},
	}
	plain := newTestService(t, host)
	defer plain.Close()
	var env pluginabi.Envelope
	_ = json.Unmarshal(plain.Call(pluginabi.MethodQuotaFetch, []byte(`{"auth_index":"idx-2","provider":"opendesign"}`)), &env)
	if env.OK || env.Error == nil || env.Error.Code != "account_not_signed_in" {
		t.Fatalf("unsigned fetch = %+v", env)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_control_key","message":"control key revoked"}}`)
	}))
	defer srv.Close()
	svc := signedIn(t, &fakeAccountAPI{})
	svc.velaAPIURL = srv.URL
	env = pluginabi.Envelope{}
	_ = json.Unmarshal(svc.Call(pluginabi.MethodQuotaFetch, []byte(`{"auth_index":"idx-1","provider":"opendesign"}`)), &env)
	if env.OK || env.Error == nil || env.Error.Code != "account_login_expired" || env.Error.HTTPStatus != http.StatusUnauthorized || !strings.Contains(env.Error.Message, "control key revoked") {
		t.Fatalf("expired fetch = %+v", env.Error)
	}
}

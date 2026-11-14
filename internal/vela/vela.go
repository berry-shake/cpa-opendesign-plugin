// Package vela talks to the OpenDesign account API (amr-api.open-design.ai)
// the way the official vela CLI does: a device authorization signs the
// account in, and the control key it issues reads the account's plan
// windows and balance.
//
// Requests copy what vela 0.1.10 sends: the same body fields, Content-Type
// only when there is a body, Authorization only with a key,
// x-vela-workspace-id only for workspace calls, and no User-Agent or Accept
// of its own, so Go's default User-Agent goes out as it does from vela.
package vela

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultAPIURL is the account API of vela's prod profile.
	DefaultAPIURL = "https://amr-api.open-design.ai"
	// CLIVersion is the vela release whose requests these copy; vela sends
	// its own version as cliVersion when it starts a device authorization.
	CLIVersion = "0.1.10"
	// Profile is vela's default profile name.
	Profile = "prod"

	maxResponseBody = 1 << 20
)

// Device authorization states the token endpoint reports.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusCancelled = "cancelled"
	StatusExpired   = "expired"
	StatusConsumed  = "consumed"
)

// Client calls the account API through one HTTP client (and so one egress).
type Client struct {
	HTTP *http.Client
	// APIURL is the API origin; empty means DefaultAPIURL.
	APIURL string
}

// Error is a non-2xx answer of the account API.
type Error struct {
	Status  int
	Code    string
	Message string
	// Blocked is set when the answer is an HTML page rather than the API's
	// JSON: Cloudflare in front of the API refused the request before it
	// reached the API. Cloudflare refuses Go's User-Agent on
	// /api/v1/billing/summary, for one.
	Blocked bool
}

func (e *Error) Error() string {
	text := strings.TrimSpace(e.Message)
	if text == "" {
		text = http.StatusText(e.Status)
	}
	if e.Code != "" && !strings.Contains(text, e.Code) {
		text = e.Code + ": " + text
	}
	if e.Blocked {
		return fmt.Sprintf("blocked before reaching the OpenDesign account API (HTTP %d: %s)", e.Status, text)
	}
	return fmt.Sprintf("OpenDesign account API HTTP %d: %s", e.Status, text)
}

// Unauthorized reports whether err is the account API itself rejecting the
// control key (HTTP 401 or 403 with the API's JSON body): the login expired
// or was revoked. A page Cloudflare answers with is not.
func Unauthorized(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && !apiErr.Blocked && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden)
}

func (c Client) origin() string {
	base := strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
	if base == "" {
		return DefaultAPIURL
	}
	return base
}

// do sends one request. body nil means no body; key "" means no
// Authorization; workspaceID "" means no workspace header.
func (c Client) do(ctx context.Context, method, path, key, workspaceID string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("marshal request body: %w", errMarshal)
		}
		reader = bytes.NewReader(raw)
	}
	req, errReq := http.NewRequestWithContext(ctx, method, c.origin()+path, reader)
	if errReq != nil {
		return fmt.Errorf("build request: %w", errReq)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key = strings.TrimSpace(key); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if workspaceID = strings.TrimSpace(workspaceID); workspaceID != "" {
		req.Header.Set("x-vela-workspace-id", workspaceID)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return fmt.Errorf("%s %s: %w", method, path, unwrapURLError(errDo))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if errRead != nil {
		return fmt.Errorf("read response body: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if errDecode := json.Unmarshal(raw, out); errDecode != nil {
		return fmt.Errorf("decode response JSON: %w", errDecode)
	}
	return nil
}

func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

var htmlTitle = regexp.MustCompile(`(?is)<title>\s*(.*?)\s*</title>`)

// apiError reads {"error":{"code","message"}}, {"code","message"} or
// {"error":"..."} bodies; an HTML page is a block in front of the API and
// is named by its title; anything else is kept as text.
func apiError(status int, raw []byte) *Error {
	out := &Error{Status: status}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '<' {
		out.Blocked = true
		if m := htmlTitle.FindSubmatch(trimmed); m != nil {
			out.Message = strings.Join(strings.Fields(string(m[1])), " ")
		} else {
			out.Message = "HTML page"
		}
		return out
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) == nil {
		out.Code, out.Message = text(body["code"]), text(body["message"])
		switch inner := body["error"].(type) {
		case map[string]any:
			if out.Code == "" {
				out.Code = text(inner["code"])
			}
			if out.Message == "" {
				out.Message = text(inner["message"])
			}
		case string:
			if out.Message == "" {
				out.Message = inner
			} else if out.Code == "" {
				out.Code = inner
			}
		}
	}
	if out.Message == "" && out.Code == "" {
		message := strings.TrimSpace(string(raw))
		if len(message) > 300 {
			message = message[:300] + "…"
		}
		out.Message = message
	}
	return out
}

func text(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// ---- device authorization ----

// DeviceAuthorization is a started sign-in: the user opens ActivationURL,
// confirms UserCode, and the device polls with DeviceSecret.
type DeviceAuthorization struct {
	DeviceID            string    `json:"deviceId"`
	UserCode            string    `json:"userCode"`
	DeviceSecret        string    `json:"deviceSecret"`
	ActivationURL       string    `json:"activationUrl"`
	PollIntervalSeconds int       `json:"pollIntervalSeconds"`
	Status              string    `json:"status"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

type capabilities struct {
	CompletionReceipt bool `json:"completionReceipt"`
}

type createDeviceAuthorizationRequest struct {
	Capabilities capabilities `json:"capabilities"`
	CLIVersion   string       `json:"cliVersion"`
	Host         string       `json:"host"`
	Profile      string       `json:"profile"`
}

// StartDeviceAuthorization starts a sign-in as `vela login` does. host is
// the machine's host name, which the account lists the device under.
func (c Client) StartDeviceAuthorization(ctx context.Context, host string) (DeviceAuthorization, error) {
	var out DeviceAuthorization
	body := createDeviceAuthorizationRequest{
		Capabilities: capabilities{CompletionReceipt: true},
		CLIVersion:   CLIVersion,
		Host:         host,
		Profile:      Profile,
	}
	if errDo := c.do(ctx, http.MethodPost, "/api/v1/cli/device-authorizations", "", "", body, &out); errDo != nil {
		return out, errDo
	}
	if out.DeviceID == "" || out.UserCode == "" || out.DeviceSecret == "" || out.ActivationURL == "" {
		return out, errors.New("device authorization response missing required fields")
	}
	if out.PollIntervalSeconds <= 0 {
		return out, errors.New("device authorization response pollIntervalSeconds must be positive")
	}
	return out, nil
}

// User is the account a control key belongs to.
type User struct {
	ID         string `json:"id"`
	AuthUserID string `json:"authUserId,omitempty"`
	Email      string `json:"email"`
	Name       string `json:"name,omitempty"`
}

// Token is the token endpoint's answer. The keys are set once Status is
// approved.
type Token struct {
	Status       string `json:"status"`
	ControlKey   string `json:"controlKey"`
	ControlKeyID string `json:"controlKeyId"`
	RuntimeKey   string `json:"runtimeKey"`
	User         *User  `json:"user"`
	APIURL       string `json:"apiUrl"`
	LinkURL      string `json:"linkUrl"`
}

type deviceSecretBody struct {
	DeviceSecret string `json:"deviceSecret"`
}

type tokenRequest struct {
	DeviceSecret            string `json:"deviceSecret"`
	ClaimRegistrationInvite bool   `json:"claimRegistrationInvite"`
}

// PollToken asks whether the user confirmed the sign-in.
func (c Client) PollToken(ctx context.Context, deviceID, deviceSecret string) (Token, error) {
	var out Token
	body := tokenRequest{DeviceSecret: deviceSecret}
	errDo := c.do(ctx, http.MethodPost, "/api/v1/cli/device-authorizations/"+url.PathEscape(deviceID)+"/token", "", "", body, &out)
	return out, errDo
}

// Validate checks an approved token the way vela does before saving it.
func (t Token) Validate() error {
	switch {
	case t.Status != StatusApproved:
		return errors.New("token response status must be approved")
	case strings.TrimSpace(t.ControlKey) == "":
		return errors.New("approved token response missing controlKey")
	case strings.TrimSpace(t.RuntimeKey) == "":
		return errors.New("approved token response missing runtimeKey")
	case t.User == nil || strings.TrimSpace(t.User.ID) == "":
		return errors.New("approved token response missing user")
	}
	for name, raw := range map[string]string{"apiUrl": t.APIURL, "linkUrl": t.LinkURL} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if parsed, errParse := url.Parse(raw); errParse != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("approved token response %s is not an https URL", name)
		}
	}
	return nil
}

// CompleteDeviceAuthorization tells the API the keys were saved; a device
// that announced completionReceipt sends it after storing the keys.
func (c Client) CompleteDeviceAuthorization(ctx context.Context, deviceID, deviceSecret string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/cli/device-authorizations/"+url.PathEscape(deviceID)+"/complete", "", "", deviceSecretBody{DeviceSecret: deviceSecret}, nil)
}

// CancelDeviceAuthorization abandons a sign-in that has not been approved.
func (c Client) CancelDeviceAuthorization(ctx context.Context, deviceID, deviceSecret string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/cli/device-authorizations/"+url.PathEscape(deviceID)+"/cancel", "", "", deviceSecretBody{DeviceSecret: deviceSecret}, nil)
}

type logoutRequest struct {
	ControlKeyID      string `json:"controlKeyId"`
	ExpectedAppUserID string `json:"expectedAppUserId"`
}

// Logout revokes a control key, as `vela logout` does.
func (c Client) Logout(ctx context.Context, controlKey, controlKeyID, userID string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/cli/logout", controlKey, "", logoutRequest{ControlKeyID: controlKeyID, ExpectedAppUserID: userID}, nil)
}

// ---- workspaces and billing ----

// Workspace is one membership of the account.
type Workspace struct {
	WorkspaceID       string `json:"workspaceId"`
	WorkspaceName     string `json:"workspaceName"`
	WorkspaceType     string `json:"workspaceType"`
	WorkspaceMemberID string `json:"workspaceMemberId"`
	Role              string `json:"role"`
	MemberStatus      string `json:"memberStatus"`
	LifecycleState    string `json:"lifecycleState"`
	IsDefault         bool   `json:"isDefault"`
}

// Workspaces lists the account's workspaces.
func (c Client) Workspaces(ctx context.Context, controlKey string) ([]Workspace, error) {
	var out struct {
		Items []Workspace `json:"items"`
	}
	if errDo := c.do(ctx, http.MethodGet, "/api/v1/workspaces", controlKey, "", nil, &out); errDo != nil {
		return nil, errDo
	}
	return out.Items, nil
}

// PickWorkspace chooses the workspace whose plan to show, as the official
// client does: an active personal workspace, else the default one, else the
// first active one.
func PickWorkspace(items []Workspace) (Workspace, bool) {
	var active []Workspace
	for _, item := range items {
		if item.WorkspaceID == "" {
			continue
		}
		if item.MemberStatus != "" && item.MemberStatus != "active" {
			continue
		}
		if item.LifecycleState != "" && item.LifecycleState != "active" {
			continue
		}
		active = append(active, item)
	}
	for _, item := range active {
		if item.WorkspaceType == "personal" {
			return item, true
		}
	}
	for _, item := range active {
		if item.IsDefault {
			return item, true
		}
	}
	if len(active) > 0 {
		return active[0], true
	}
	return Workspace{}, false
}

// PlanWindow is one rolling allowance of the coding plan. Credits are
// decimal strings.
type PlanWindow struct {
	PolicyID         string  `json:"policyId"`
	DurationSeconds  int64   `json:"durationSeconds"`
	ResetMode        string  `json:"resetMode"`
	UsedCredits      string  `json:"usedCredits"`
	LimitCredits     string  `json:"limitCredits"`
	RemainingCredits string  `json:"remainingCredits"`
	WindowStart      *string `json:"windowStart"`
	ResetsAt         *string `json:"resetsAt"`
}

// CodingPlan is the workspace's coding plan and its windows.
type CodingPlan struct {
	WorkspaceID string       `json:"workspaceId"`
	GeneratedAt string       `json:"generatedAt"`
	Eligible    bool         `json:"eligible"`
	Tier        *string      `json:"tier"`
	Windows     []PlanWindow `json:"windows"`
}

// FundingCheck is what the workspace may spend on next: the coding plan's
// windows and the wallet balance.
type FundingCheck struct {
	SchemaVersion     int        `json:"schemaVersion"`
	WorkspaceID       string     `json:"workspaceId"`
	WorkspaceMemberID string     `json:"workspaceMemberId"`
	GeneratedAt       string     `json:"generatedAt"`
	BalanceUSD        string     `json:"balanceUsd"`
	CodingPlan        CodingPlan `json:"codingPlan"`
	ModelCovered      *bool      `json:"modelCovered"`
	Funding           string     `json:"funding"`
	FundingCheck      *string    `json:"fundingCheck"`
}

// FundingCheck reads the workspace's coding plan windows and balance
// (vela billing funding-check).
func (c Client) FundingCheck(ctx context.Context, controlKey, workspaceID string) (FundingCheck, error) {
	var out FundingCheck
	if strings.TrimSpace(workspaceID) == "" {
		return out, errors.New("workspaceId is required")
	}
	errDo := c.do(ctx, http.MethodGet, "/api/v1/workspaces/"+url.PathEscape(workspaceID)+"/billing/funding-check", controlKey, workspaceID, nil, &out)
	return out, errDo
}

// SnapshotPlan is the workspace's subscription.
type SnapshotPlan struct {
	Tier               string `json:"tier"`
	Interval           string `json:"interval"`
	CurrentPeriodStart string `json:"currentPeriodStart"`
	CurrentPeriodEnd   string `json:"currentPeriodEnd"`
	CancelAtPeriodEnd  bool   `json:"cancelAtPeriodEnd"`
	Status             string `json:"status"`
}

// SnapshotWallet is the workspace's wallet.
type SnapshotWallet struct {
	BalanceUSD string  `json:"balanceUsd"`
	ExpiresAt  *string `json:"expiresAt"`
}

// DesignPlanWindow is a plan window as the snapshot reports it.
type DesignPlanWindow struct {
	Kind             string  `json:"kind"`
	WindowStart      *string `json:"windowStart"`
	ResetsAt         *string `json:"resetsAt"`
	RemainingCredits string  `json:"remainingCredits"`
	LimitCredits     string  `json:"limitCredits"`
}

// Snapshot is the workspace's billing state: plan, wallet and windows
// (vela billing workspace-snapshot).
type Snapshot struct {
	SchemaVersion     int                `json:"schemaVersion"`
	WorkspaceID       string             `json:"workspaceId"`
	Plan              *SnapshotPlan      `json:"plan"`
	Wallet            SnapshotWallet     `json:"wallet"`
	DesignPlanWindows []DesignPlanWindow `json:"designPlanWindows"`
}

// Snapshot reads the workspace's billing snapshot.
func (c Client) Snapshot(ctx context.Context, controlKey, workspaceID string) (Snapshot, error) {
	var out Snapshot
	if strings.TrimSpace(workspaceID) == "" {
		return out, errors.New("workspaceId is required")
	}
	errDo := c.do(ctx, http.MethodGet, "/api/v1/workspaces/"+url.PathEscape(workspaceID)+"/billing/snapshot", controlKey, workspaceID, nil, &out)
	return out, errDo
}

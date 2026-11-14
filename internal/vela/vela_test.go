package vela

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type seen struct {
	method, path string
	header       http.Header
	body         string
}

// recorder answers every path with the given JSON and keeps the requests.
func recorder(t *testing.T, answers map[string]string) (*httptest.Server, func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: string(raw)})
		mu.Unlock()
		answer, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, `{"error":{"code":"not_found","message":"no route"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}
}

// The requests copy vela 0.1.10: its body fields, Content-Type only with a
// body, Authorization only with a key, the workspace header only for
// workspace calls, and Go's own User-Agent with no Accept header.
func TestRequestsMatchVela(t *testing.T) {
	srv, requests := recorder(t, map[string]string{
		"POST /api/v1/cli/device-authorizations":             `{"deviceId":"dev-1","userCode":"ABCD-EFGH","deviceSecret":"sec","activationUrl":"https://open-design.ai/cli/activate?deviceId=dev-1","pollIntervalSeconds":5,"status":"pending","expiresAt":"2026-11-14T00:00:00Z"}`,
		"GET /api/v1/workspaces":                             `{"items":[]}`,
		"GET /api/v1/workspaces/ws-1/billing/funding-check":  `{"codingPlan":{"windows":[]}}`,
		"POST /api/v1/cli/device-authorizations/dev-1/token": `{"status":"pending"}`,
		"POST /api/v1/cli/logout":                            `{}`,
	})
	client := Client{HTTP: srv.Client(), APIURL: srv.URL}
	ctx := context.Background()
	device, err := client.StartDeviceAuthorization(ctx, "4bb2bdffb36f")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if device.DeviceID != "dev-1" || device.UserCode != "ABCD-EFGH" || device.PollIntervalSeconds != 5 || device.ExpiresAt.IsZero() {
		t.Fatalf("device = %+v", device)
	}
	if _, err := client.PollToken(ctx, device.DeviceID, device.DeviceSecret); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if _, err := client.Workspaces(ctx, "ck"); err != nil {
		t.Fatalf("workspaces: %v", err)
	}
	if _, err := client.FundingCheck(ctx, "ck", "ws-1"); err != nil {
		t.Fatalf("funding: %v", err)
	}
	if err := client.Logout(ctx, "ck", "ckid", "user-1"); err != nil {
		t.Fatalf("logout: %v", err)
	}

	got := requests()
	if len(got) != 5 {
		t.Fatalf("requests = %d", len(got))
	}
	wantBodies := []string{
		`{"capabilities":{"completionReceipt":true},"cliVersion":"0.1.10","host":"4bb2bdffb36f","profile":"prod"}`,
		`{"deviceSecret":"sec","claimRegistrationInvite":false}`,
		``,
		``,
		`{"controlKeyId":"ckid","expectedAppUserId":"user-1"}`,
	}
	wantAuth := []string{"", "", "Bearer ck", "Bearer ck", "Bearer ck"}
	wantWorkspace := []string{"", "", "", "ws-1", ""}
	for i, r := range got {
		if r.body != wantBodies[i] {
			t.Errorf("%s %s body = %s, want %s", r.method, r.path, r.body, wantBodies[i])
		}
		if ua := r.header.Get("User-Agent"); !strings.HasPrefix(ua, "Go-http-client/") {
			t.Errorf("%s %s User-Agent = %q, want Go's default as vela sends", r.method, r.path, ua)
		}
		if accept := r.header.Get("Accept"); accept != "" {
			t.Errorf("%s %s sends Accept %q; vela sends none", r.method, r.path, accept)
		}
		wantType := ""
		if wantBodies[i] != "" {
			wantType = "application/json"
		}
		if ct := r.header.Get("Content-Type"); ct != wantType {
			t.Errorf("%s %s Content-Type = %q, want %q", r.method, r.path, ct, wantType)
		}
		if auth := r.header.Get("Authorization"); auth != wantAuth[i] {
			t.Errorf("%s %s Authorization = %q, want %q", r.method, r.path, auth, wantAuth[i])
		}
		if ws := r.header.Get("X-Vela-Workspace-Id"); ws != wantWorkspace[i] {
			t.Errorf("%s %s x-vela-workspace-id = %q, want %q", r.method, r.path, ws, wantWorkspace[i])
		}
	}
}

func TestStartRejectsIncompleteResponse(t *testing.T) {
	srv, _ := recorder(t, map[string]string{"POST /api/v1/cli/device-authorizations": `{"deviceId":"d","userCode":"c","deviceSecret":"s","activationUrl":"https://x","pollIntervalSeconds":0}`})
	if _, err := (Client{HTTP: srv.Client(), APIURL: srv.URL}).StartDeviceAuthorization(context.Background(), "h"); err == nil {
		t.Fatal("a zero poll interval must be rejected")
	}
}

func TestErrorsAndUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_control_key","message":"control key revoked"}}`)
	}))
	defer srv.Close()
	_, err := (Client{HTTP: srv.Client(), APIURL: srv.URL}).FundingCheck(context.Background(), "ck", "ws")
	if !Unauthorized(err) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if !strings.Contains(err.Error(), "control key revoked") || !strings.Contains(err.Error(), "invalid_control_key") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestTokenValidate(t *testing.T) {
	ok := Token{Status: StatusApproved, ControlKey: "ck", RuntimeKey: "rk", User: &User{ID: "u"}, APIURL: "https://amr-api.open-design.ai", LinkURL: "https://amr-link.open-design.ai"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid token: %v", err)
	}
	for name, mutate := range map[string]func(*Token){
		"pending":        func(t *Token) { t.Status = StatusPending },
		"no control key": func(t *Token) { t.ControlKey = "" },
		"no runtime key": func(t *Token) { t.RuntimeKey = "" },
		"no user":        func(t *Token) { t.User = nil },
		"http api":       func(t *Token) { t.APIURL = "http://evil.example" },
	} {
		bad := ok
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestPickWorkspace(t *testing.T) {
	items := []Workspace{
		{WorkspaceID: "team", WorkspaceType: "team", MemberStatus: "active", LifecycleState: "active", IsDefault: true},
		{WorkspaceID: "gone", WorkspaceType: "personal", MemberStatus: "removed", LifecycleState: "active"},
		{WorkspaceID: "mine", WorkspaceType: "personal", MemberStatus: "active", LifecycleState: "active"},
	}
	if got, _ := PickWorkspace(items); got.WorkspaceID != "mine" {
		t.Fatalf("picked %q, want the active personal workspace", got.WorkspaceID)
	}
	if got, _ := PickWorkspace(items[:2]); got.WorkspaceID != "team" {
		t.Fatalf("picked %q, want the default one", got.WorkspaceID)
	}
	if _, ok := PickWorkspace(items[1:2]); ok {
		t.Fatal("an inactive membership must not be picked")
	}
}

func TestFundingCheckDecodes(t *testing.T) {
	srv, _ := recorder(t, map[string]string{"GET /api/v1/workspaces/ws/billing/funding-check": `{"schemaVersion":1,"workspaceId":"ws","balanceUsd":"3.50","funding":"coding_plan","codingPlan":{"eligible":true,"tier":"go","windows":[{"policyId":"p5","durationSeconds":18000,"resetMode":"activity_triggered","usedCredits":"120","limitCredits":"1000","remainingCredits":"880","windowStart":"2026-11-13T10:00:00Z","resetsAt":"2026-11-13T15:00:00Z"}]}}`})
	got, err := (Client{HTTP: srv.Client(), APIURL: srv.URL}).FundingCheck(context.Background(), "ck", "ws")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.CodingPlan)
	if got.CodingPlan.Tier == nil || *got.CodingPlan.Tier != "go" || len(got.CodingPlan.Windows) != 1 || got.CodingPlan.Windows[0].RemainingCredits != "880" {
		t.Fatalf("decoded %s", raw)
	}
}

// Cloudflare's HTML 403 page is a block in front of the API, not the API
// rejecting the key; the error names the page by its title.
func TestCloudflareBlockIsNotUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<!DOCTYPE html>\n<!--[if lt IE 7]> <html class=\"no-js\"> <![endif]-->\n<head>\n<title>Attention Required! |\n Cloudflare</title>\n</head><body>Sorry, you have been blocked</body></html>")
	}))
	defer srv.Close()
	_, err := (Client{HTTP: srv.Client(), APIURL: srv.URL}).Snapshot(context.Background(), "ck", "ws")
	if err == nil || Unauthorized(err) {
		t.Fatalf("err = %v, want a block that is not unauthorized", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "Attention Required! | Cloudflare") || !strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "<") {
		t.Fatalf("message = %q", msg)
	}
}

func TestSnapshotDecodes(t *testing.T) {
	srv, requests := recorder(t, map[string]string{"GET /api/v1/workspaces/ws/billing/snapshot": `{"schemaVersion":1,"workspaceId":"ws","plan":{"tier":"go","interval":"month","currentPeriodStart":"2026-11-13T16:06:05Z","currentPeriodEnd":"2026-12-13T16:06:05Z","cancelAtPeriodEnd":true,"status":"active"},"wallet":{"balanceUsd":"1.0000","expiresAt":null},"designPlanWindows":[{"kind":"5h","windowStart":null,"resetsAt":null,"remainingCredits":"50000","limitCredits":"50000"}]}`})
	got, err := (Client{HTTP: srv.Client(), APIURL: srv.URL}).Snapshot(context.Background(), "ck", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan == nil || got.Plan.CurrentPeriodEnd != "2026-12-13T16:06:05Z" || !got.Plan.CancelAtPeriodEnd || got.Wallet.BalanceUSD != "1.0000" || len(got.DesignPlanWindows) != 1 {
		t.Fatalf("decoded %+v", got)
	}
	r := requests()[0]
	if r.header.Get("Authorization") != "Bearer ck" || r.header.Get("X-Vela-Workspace-Id") != "ws" || r.header.Get("Content-Type") != "" {
		t.Fatalf("headers = %v", r.header)
	}
}

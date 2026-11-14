package opendesign

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/vela"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/provider"
)

// The plan windows of the OpenDesign account signed in on a key card, read
// with its control key from the account API (vela billing funding-check and
// workspace-snapshot).

// accountCacheTTL is how long a read of the account's windows is reused.
const accountCacheTTL = 30 * time.Second

// accountLink is what the card shows about a signed-in account without
// reading its windows. The control key itself is never shown.
type accountLink struct {
	Email         string `json:"email,omitempty"`
	Name          string `json:"name,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	LoggedInAt    string `json:"logged_in_at,omitempty"`
}

func accountLinkOf(a Account) *accountLink {
	if !a.LoggedIn() {
		return nil
	}
	return &accountLink{Email: a.Email, Name: a.Name, WorkspaceName: a.WorkspaceName, LoggedInAt: a.LoggedInAt}
}

// planWindowView is one coding plan window in credits.
type planWindowView struct {
	// Kind is "5h", "7d" or the duration in seconds ("<n>s") for others.
	Kind            string  `json:"kind"`
	DurationSeconds int64   `json:"duration_seconds"`
	Used            float64 `json:"used"`
	Limit           float64 `json:"limit"`
	Remaining       float64 `json:"remaining"`
	WindowStart     string  `json:"window_start,omitempty"`
	ResetsAt        string  `json:"resets_at,omitempty"`
}

// accountUsageView is the account's plan and windows.
type accountUsageView struct {
	Email         string `json:"email,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	// Tier is the coding plan (go, plus, pro, max); Eligible is false when
	// the workspace has no plan allowance.
	Tier       string           `json:"tier,omitempty"`
	Eligible   bool             `json:"eligible"`
	Funding    string           `json:"funding,omitempty"`
	BalanceUSD string           `json:"balance_usd,omitempty"`
	Windows    []planWindowView `json:"windows"`

	// The subscription from the billing snapshot; empty when it could not
	// be read, which leaves the windows as they are.
	SubscriptionStatus string `json:"subscription_status,omitempty"`
	// Interval is the billing period (month, year).
	Interval          string `json:"interval,omitempty"`
	PeriodEnd         string `json:"period_end,omitempty"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end,omitempty"`

	FetchedAt string `json:"fetched_at,omitempty"`
	// Error says why the windows could not be read; Expired is set when the
	// account API itself rejected the control key, so the card asks to sign
	// in again.
	Error   string `json:"error,omitempty"`
	Expired bool   `json:"expired,omitempty"`
}

type accountCacheEntry struct {
	key  string
	at   time.Time
	view accountUsageView
}

type accountCache struct {
	mu      sync.Mutex
	entries map[string]accountCacheEntry
	// workspaces remembers the workspace picked for logins saved without one.
	workspaces map[string]vela.Workspace
}

func newAccountCache() *accountCache {
	return &accountCache{entries: make(map[string]accountCacheEntry), workspaces: make(map[string]vela.Workspace)}
}

func (c *accountCache) get(authIndex, key string, now time.Time) (accountUsageView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[authIndex]
	if !ok || entry.key != key || now.Sub(entry.at) > accountCacheTTL {
		return accountUsageView{}, false
	}
	return entry.view, true
}

func (c *accountCache) put(authIndex, key string, now time.Time, view accountUsageView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[authIndex] = accountCacheEntry{key: key, at: now, view: view}
}

func (c *accountCache) forget(authIndex string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, authIndex)
	delete(c.workspaces, authIndex)
}

func (c *accountCache) workspace(authIndex string) (vela.Workspace, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.workspaces[authIndex]
	return w, ok
}

func (c *accountCache) setWorkspace(authIndex string, w vela.Workspace) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workspaces[authIndex] = w
}

// accountUsage reads the windows of the account signed in on a credential;
// nil when none is. Failures are reported in the view, not as an error, so
// the card keeps its log figures.
func (a *Adapter) accountUsage(entry provider.AuthFile, force bool) *accountUsageView {
	account := accountOf(entry.Credential)
	if !account.LoggedIn() {
		return nil
	}
	now := a.now()
	cacheKey := credential.Fingerprint(account.ControlKey) + "|" + account.WorkspaceID
	if !force {
		if cached, ok := a.accounts.get(entry.AuthIndex, cacheKey, now); ok {
			return &cached
		}
	}
	view := accountUsageView{Email: account.Email, WorkspaceName: account.WorkspaceName, Windows: []planWindowView{}}
	client, errClient := a.velaClient(entry.Credential.ProxyURL, account.APIURL)
	if errClient != nil {
		view.Error = errClient.Error()
		return &view
	}
	ctx, cancel := context.WithTimeout(a.host.Env().Context, 2*provider.AccountCallTimeout)
	defer cancel()
	a.readAccount(ctx, client, entry.AuthIndex, account, &view)
	view.FetchedAt = a.now().UTC().Format(time.RFC3339)
	if view.Error == "" || view.Expired {
		a.accounts.put(entry.AuthIndex, cacheKey, now, view)
	}
	return &view
}

func (a *Adapter) readAccount(ctx context.Context, client vela.Client, authIndex string, account Account, view *accountUsageView) {
	fail := func(err error) {
		view.Error = err.Error()
		view.Expired = vela.Unauthorized(err)
	}
	workspaceID := account.WorkspaceID
	if workspaceID == "" {
		if picked, ok := a.accounts.workspace(authIndex); ok {
			workspaceID, view.WorkspaceName = picked.WorkspaceID, picked.WorkspaceName
		} else {
			items, errList := client.Workspaces(ctx, account.ControlKey)
			if errList != nil {
				fail(fmt.Errorf("list workspaces: %w", errList))
				return
			}
			picked, ok := vela.PickWorkspace(items)
			if !ok {
				view.Error = "the OpenDesign account has no active workspace"
				return
			}
			a.accounts.setWorkspace(authIndex, picked)
			workspaceID, view.WorkspaceName = picked.WorkspaceID, picked.WorkspaceName
		}
	}

	funding, errFunding := client.FundingCheck(ctx, account.ControlKey, workspaceID)
	if errFunding != nil {
		fail(fmt.Errorf("read plan windows: %w", errFunding))
		return
	}
	view.Eligible = funding.CodingPlan.Eligible
	view.Funding = funding.Funding
	view.BalanceUSD = funding.BalanceUSD
	if funding.CodingPlan.Tier != nil {
		view.Tier = *funding.CodingPlan.Tier
	}
	for _, w := range funding.CodingPlan.Windows {
		view.Windows = append(view.Windows, planWindowViewOf(w))
	}

	// The snapshot adds when the subscription renews; the windows stand
	// without it, so a failure here is not reported.
	snapshot, errSnapshot := client.Snapshot(ctx, account.ControlKey, workspaceID)
	if errSnapshot != nil || snapshot.Plan == nil {
		return
	}
	view.SubscriptionStatus = strings.TrimSpace(snapshot.Plan.Status)
	view.Interval = strings.TrimSpace(snapshot.Plan.Interval)
	view.PeriodEnd = strings.TrimSpace(snapshot.Plan.CurrentPeriodEnd)
	view.CancelAtPeriodEnd = snapshot.Plan.CancelAtPeriodEnd
	if view.Tier == "" {
		view.Tier = strings.TrimSpace(snapshot.Plan.Tier)
	}
}

func planWindowViewOf(w vela.PlanWindow) planWindowView {
	view := planWindowView{
		DurationSeconds: w.DurationSeconds,
		Used:            credits(w.UsedCredits),
		Limit:           credits(w.LimitCredits),
		Remaining:       credits(w.RemainingCredits),
		WindowStart:     deref(w.WindowStart),
		ResetsAt:        deref(w.ResetsAt),
	}
	switch w.DurationSeconds {
	case 5 * 3600:
		view.Kind = "5h"
	case 7 * 24 * 3600:
		view.Kind = "7d"
	default:
		view.Kind = strconv.FormatInt(w.DurationSeconds, 10) + "s"
	}
	return view
}

// credits reads a decimal credit string; the API sends big integers as
// strings, which a float64 shows well enough. "Inf" and numbers too large for
// a float64 are no amount, and JSON cannot carry them, so they read as 0.
func credits(raw string) float64 {
	value, ok := new(big.Float).SetString(strings.TrimSpace(raw))
	if !ok {
		return 0
	}
	out, _ := value.Float64()
	if math.IsInf(out, 0) || math.IsNaN(out) {
		return 0
	}
	return out
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

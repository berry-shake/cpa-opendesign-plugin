package plugin

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/credential"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/vela"
)

// The plan windows of the OpenDesign account signed in on a key card, read
// with its control key from the account API (vela billing funding-check and
// summary).

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

func accountLinkOf(a credential.Account) *accountLink {
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

// accountUsageView is the account's plan, windows and spending.
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

	MembershipTier     string `json:"membership_tier,omitempty"`
	SubscriptionStatus string `json:"subscription_status,omitempty"`
	PeriodEnd          string `json:"period_end,omitempty"`
	TodayConsumedUSD   string `json:"today_consumed_usd,omitempty"`
	TotalConsumedUSD   string `json:"total_consumed_usd,omitempty"`
	UsageCount         int64  `json:"usage_count,omitempty"`

	FetchedAt string `json:"fetched_at,omitempty"`
	// Error says why the windows could not be read; Expired is set when the
	// account API rejected the control key, so the card asks to sign in
	// again.
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
func (s *Service) accountUsage(entry opendesignAuth, force bool) *accountUsageView {
	account := entry.cred.Account
	if !account.LoggedIn() {
		return nil
	}
	now := s.now()
	cacheKey := credential.Fingerprint(account.ControlKey) + "|" + account.WorkspaceID
	if !force {
		if cached, ok := s.accounts.get(entry.AuthIndex, cacheKey, now); ok {
			return &cached
		}
	}
	view := accountUsageView{Email: account.Email, WorkspaceName: account.WorkspaceName, Windows: []planWindowView{}}
	client, errClient := s.velaClient(entry.cred.ProxyURL, account.APIURL)
	if errClient != nil {
		view.Error = errClient.Error()
		return &view
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*accountCallTimeout)
	defer cancel()
	s.readAccount(ctx, client, entry.AuthIndex, account, &view)
	view.FetchedAt = s.now().UTC().Format(time.RFC3339)
	if view.Error == "" || view.Expired {
		s.accounts.put(entry.AuthIndex, cacheKey, now, view)
	}
	return &view
}

func (s *Service) readAccount(ctx context.Context, client vela.Client, authIndex string, account credential.Account, view *accountUsageView) {
	fail := func(err error) {
		view.Error = err.Error()
		if vela.Unauthorized(err) {
			view.Expired = true
			view.Error = "the OpenDesign account login expired or was revoked; sign in again"
		}
	}
	workspaceID := account.WorkspaceID
	if workspaceID == "" {
		if picked, ok := s.accounts.workspace(authIndex); ok {
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
			s.accounts.setWorkspace(authIndex, picked)
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

	// The summary adds membership and spending; the windows stand without it.
	summary, errSummary := client.Summary(ctx, account.ControlKey)
	if errSummary != nil {
		if vela.Unauthorized(errSummary) {
			fail(errSummary)
		}
		return
	}
	view.MembershipTier = deref(summary.MembershipTier)
	view.SubscriptionStatus = deref(summary.SubscriptionStatus)
	view.PeriodEnd = deref(summary.SubscriptionCurrentPeriodEnd)
	view.TodayConsumedUSD = summary.TodayConsumedUSD
	view.TotalConsumedUSD = summary.TotalConsumedUSD
	view.UsageCount = summary.UsageCount
	if view.BalanceUSD == "" {
		view.BalanceUSD = summary.BalanceUSD
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
// strings, which a float64 shows well enough.
func credits(raw string) float64 {
	value, ok := new(big.Float).SetString(strings.TrimSpace(raw))
	if !ok {
		return 0
	}
	out, _ := value.Float64()
	return out
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

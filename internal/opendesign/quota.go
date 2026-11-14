package opendesign

import (
	"net/http"
	"strings"
	"time"

	"github.com/berry-shake/cpa-provider-core/requestlog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// CPA's quota provider: the auth file page of the management panel shows the
// plan windows of the account signed in on a key, as it does for the
// built-in providers.

// FetchQuota answers quota.fetch for one OpenDesign credential with the
// windows the key card shows, read fresh.
func (a *Adapter) FetchQuota(req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, *pluginabi.Error) {
	var out pluginapi.QuotaFetchResponse
	if name := strings.TrimSpace(req.Provider); name != "" && !strings.EqualFold(name, ProviderID) {
		return out, &pluginabi.Error{Code: "invalid_request", Message: "not an OpenDesign credential", HTTPStatus: http.StatusBadRequest}
	}
	if strings.TrimSpace(req.AuthIndex) == "" {
		return out, &pluginabi.Error{Code: "invalid_request", Message: "auth_index is required", HTTPStatus: http.StatusBadRequest}
	}
	entries, errList := a.host.AuthFiles()
	if errList != nil {
		return out, &pluginabi.Error{Code: "host_unavailable", Message: errList.Error(), HTTPStatus: http.StatusBadGateway}
	}
	entry, found := findAuth(entries, req.AuthIndex)
	if !found {
		return out, &pluginabi.Error{Code: "credential_not_found", Message: "no OpenDesign credential matches auth_index", HTTPStatus: http.StatusNotFound}
	}
	if entry.ReadError != "" {
		return out, &pluginabi.Error{Code: "credential_unreadable", Message: entry.ReadError, HTTPStatus: http.StatusBadGateway}
	}
	account := a.accountUsage(entry, true)
	if account == nil {
		return out, &pluginabi.Error{
			Code:       "account_not_signed_in",
			Message:    "OpenDesign API keys report no allowance; sign an OpenDesign account in on this key's card on the OpenDesign plugin page to show the plan's 5-hour and 7-day windows",
			HTTPStatus: http.StatusNotFound,
		}
	}
	if account.Error != "" {
		if account.Expired {
			return out, &pluginabi.Error{Code: "account_login_expired", Message: "the OpenDesign account login expired or was revoked; sign out and sign in again on the OpenDesign plugin page (" + account.Error + ")", HTTPStatus: http.StatusUnauthorized}
		}
		return out, &pluginabi.Error{Code: "account_read_failed", Message: account.Error, HTTPStatus: http.StatusBadGateway}
	}
	return quotaResponseOf(account), nil
}

// quotaResponseOf turns the account's windows into CPA's normalized quota:
// the plan, the wallet balance and one remaining fraction per window.
func quotaResponseOf(view *accountUsageView) pluginapi.QuotaFetchResponse {
	var out pluginapi.QuotaFetchResponse
	if plan := planName(view.Tier, view.Interval); plan != "" {
		out.Subscription = &pluginapi.QuotaSubscription{Plan: plan, TierID: view.Tier}
	}
	if strings.TrimSpace(view.BalanceUSD) != "" {
		out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: "balance", Label: "Balance", Value: credits(view.BalanceUSD), Format: "currency", Currency: "USD"})
	}
	group := pluginapi.QuotaGroup{DisplayName: "OpenDesign"}
	for _, w := range view.Windows {
		if w.Limit <= 0 {
			continue
		}
		remaining := w.Remaining / w.Limit
		remaining = min(max(remaining, 0), 1)
		group.Buckets = append(group.Buckets, pluginapi.QuotaBucket{Window: w.Kind, RemainingFraction: remaining, ResetTime: w.ResetsAt})
	}
	if len(group.Buckets) > 0 {
		out.Groups = []pluginapi.QuotaGroup{group}
	}
	return out
}

// planName is "Go (Monthly)" for tier go billed monthly.
func planName(tier, interval string) string {
	tier = strings.TrimSpace(tier)
	if tier == "" {
		return ""
	}
	name := strings.ToUpper(tier[:1]) + tier[1:]
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "month", "monthly":
		name += " (Monthly)"
	case "year", "yearly", "annual":
		name += " (Yearly)"
	}
	return name
}

// usageView is a credential's usage summed from the request log, and the
// plan windows of the OpenDesign account signed in on it. API keys
// themselves report no quota.
type usageView struct {
	requestlog.Usage
	FetchedAt string            `json:"fetched_at"`
	Account   *accountUsageView `json:"account,omitempty"`
	// LogError says why the log figures are missing (log turned off or
	// unreadable) when the account's windows are shown anyway.
	LogError string `json:"log_error,omitempty"`
}

// credentialQuota serves a card's figures; force skips the short cache of
// the account's windows.
func (a *Adapter) credentialQuota(authIndex string, force bool) pluginapi.ManagementResponse {
	entries, errList := a.host.AuthFiles()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	entry, found := findAuth(entries, authIndex)
	if !found || strings.TrimSpace(authIndex) == "" {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no OpenDesign credential matches auth_index"})
	}
	now := a.now()
	usage, errUsage := a.host.Usage(entry.AuthIndex, entry.ID, now)
	var account *accountUsageView
	if entry.ReadError == "" {
		account = a.accountUsage(entry, force)
	}
	if errUsage != "" && account == nil {
		return jsonResponse(http.StatusServiceUnavailable, errorBody{Error: "log_unavailable", Message: errUsage})
	}
	// Without the log a signed-in card still shows the account's windows.
	return jsonResponse(http.StatusOK, usageView{Usage: usage, FetchedAt: now.UTC().Format(time.RFC3339), Account: account, LogError: errUsage})
}

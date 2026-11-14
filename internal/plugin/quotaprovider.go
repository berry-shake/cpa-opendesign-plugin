package plugin

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// CPA's quota provider: the auth file page of the management panel shows the
// plan windows of the account signed in on a key, as it does for the
// built-in providers.

// fetchQuota answers quota.fetch for one OpenDesign credential with the
// windows the key card shows, read fresh.
func (s *Service) fetchQuota(req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, *pluginabi.Error) {
	var out pluginapi.QuotaFetchResponse
	if provider := strings.TrimSpace(req.Provider); provider != "" && !strings.EqualFold(provider, ProviderID) {
		return out, &pluginabi.Error{Code: "invalid_request", Message: "not an OpenDesign credential", HTTPStatus: http.StatusBadRequest}
	}
	if strings.TrimSpace(req.AuthIndex) == "" {
		return out, &pluginabi.Error{Code: "invalid_request", Message: "auth_index is required", HTTPStatus: http.StatusBadRequest}
	}
	entries, errList := s.listOpenDesignAuths()
	if errList != nil {
		return out, &pluginabi.Error{Code: "host_unavailable", Message: errList.Error(), HTTPStatus: http.StatusBadGateway}
	}
	entry, found := findEntry(entries, req.AuthIndex, "")
	if !found {
		return out, &pluginabi.Error{Code: "credential_not_found", Message: "no OpenDesign credential matches auth_index", HTTPStatus: http.StatusNotFound}
	}
	if entry.readError != "" {
		return out, &pluginabi.Error{Code: "credential_unreadable", Message: entry.readError, HTTPStatus: http.StatusBadGateway}
	}
	account := s.accountUsage(entry, true)
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
func quotaResponseOf(a *accountUsageView) pluginapi.QuotaFetchResponse {
	var out pluginapi.QuotaFetchResponse
	if plan := planName(a.Tier, a.Interval); plan != "" {
		out.Subscription = &pluginapi.QuotaSubscription{Plan: plan, TierID: a.Tier}
	}
	if strings.TrimSpace(a.BalanceUSD) != "" {
		out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: "balance", Label: "Balance", Value: credits(a.BalanceUSD), Format: "currency", Currency: "USD"})
	}
	group := pluginapi.QuotaGroup{DisplayName: displayName}
	for _, w := range a.Windows {
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

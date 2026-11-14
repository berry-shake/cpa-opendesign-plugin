package opendesign

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/web"
	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Adapter is the OpenDesign provider for cpa-provider-core.
//
// Each key keeps its own model list (provider.KeyedCatalogSource). CPA's
// quota panel and the card figures show the plan windows of the OpenDesign
// account signed in on a key (QuotaFetcher, QuotaPage), which the card signs
// in with the account routes (ManagementExtension); FetchAccount and Quota
// are therefore not used.
type Adapter struct {
	catalog *Catalog
	// host is the service, set by Attach.
	host provider.Host

	// logins holds the account sign-ins started from the key cards.
	logins *loginRegistry
	// accounts caches each signed-in account's plan windows briefly.
	accounts *accountCache
	// hostname names this machine to OpenDesign when an account signs in,
	// as vela does; velaAPIURL replaces the account API origin of keys
	// signed in without one (tests point it at a fake server).
	hostname   func() (string, error)
	velaAPIURL string
}

// New returns the OpenDesign adapter with an empty catalog.
func New() *Adapter {
	return &Adapter{
		catalog:  NewCatalog(),
		logins:   newLoginRegistry(),
		accounts: newAccountCache(),
		hostname: os.Hostname,
	}
}

var (
	_ provider.Adapter             = (*Adapter)(nil)
	_ provider.ErrorRemapper       = (*Adapter)(nil)
	_ provider.CredentialExtension = (*Adapter)(nil)
	_ provider.StreamFixer         = (*Adapter)(nil)
	_ provider.HostUser            = (*Adapter)(nil)
	_ provider.ManagementExtension = (*Adapter)(nil)
	_ provider.QuotaFetcher        = (*Adapter)(nil)
	_ provider.QuotaPage           = (*Adapter)(nil)
)

// Spec describes the plugin.
func (a *Adapter) Spec() provider.Spec {
	return provider.Spec{
		ID:          ProviderID,
		DisplayName: "OpenDesign",
		Author:      "berry-shake",
		Repository:  "https://github.com/berry-shake/cpa-opendesign-plugin",
		Config: config.Profile{
			BaseURL:           "https://amr-link.open-design.ai",
			ModelPrefix:       ProviderID,
			DataDir:           "plugins/opendesign-data",
			Database:          "opendesign.db",
			BaseURLSuffixes:   []string{"/v1"},
			BaseURLHelp:       "OpenDesign inference origin (https://amr-link.open-design.ai). A trailing /v1 is dropped. Auth files may override it with base_url.",
			ModelPrefixHelp:   `Default CPA prefix for OpenDesign keys ("opendesign" -> opendesign/deepseek-v4-pro). Empty adds no prefix. An auth file's own prefix field wins.`,
			ModelsRefreshHelp: "How often to reload OpenDesign's model list; 0 disables polling.",
		},
		Page: web.Page,
		Logo: web.Logo,
		Texts: provider.Texts{
			LoginStart:          "OpenDesign uses API keys: add one on the OpenDesign plugin page or upload an opendesign auth file",
			ModelsRoute:         "OpenDesign models the keys' plans list, with inputs, context and output budgets, cost tier and which keys list each.",
			ModelsRefreshRoute:  "Reload every key's OpenDesign model list and re-register changed models.",
			AddCredentialRoute:  `Add an OpenDesign API key. Body: {"api_key","label","note","models","prefix","proxy_url"|"proxy_id"}.`,
			EditCredentialRoute: `Edit an OpenDesign credential. Body: {"auth_index","label","note","models","prefix","proxy_url"|"proxy_id","priority"}.`,
			QuotaRoute:          "Requests and tokens of one credential over the last 5 hours, 24 hours, 7 days and 30 days, summed from the request log, and the plan windows of the OpenDesign account signed in on it (?auth_index=&refresh=1).",
		},
		// StreamFix reads max_tokens.
		RequestMembers:     []string{"max_tokens"},
		CountTokensRefusal: "OpenDesign has no token counting endpoint",
		QuotaRPC: provider.QuotaRPC{
			DecodeError:  "decode quota fetch request",
			RememberHost: true,
			Reset:        &pluginapi.QuotaResetResponse{Success: false, Message: "OpenDesign plan allowance cannot be reset"},
		},
	}
}

// Endpoint is where OpenDesign serves a protocol.
func (a *Adapter) Endpoint(protocol provider.Protocol) string {
	switch protocol {
	case provider.Responses:
		return EndpointResponses
	case provider.Messages:
		return EndpointMessages
	default:
		return EndpointChat
	}
}

// Catalog is the model lists of the keys.
func (a *Adapter) Catalog() provider.CatalogSource {
	return a.catalog
}

// Attach keeps the service for the account routes, quota answers and logs.
func (a *Adapter) Attach(host provider.Host) {
	a.host = host
}

// now is the service clock.
func (a *Adapter) now() time.Time {
	return a.host.Env().Now()
}

// logf writes to CPA's log.
func (a *Adapter) logf(level, message string, fields map[string]any) {
	if a.host != nil {
		a.host.Logf(level, message, fields)
	}
}

// keyID names a key in the catalog without revealing it.
func keyID(cred credential.Credential) string {
	return credential.Fingerprint(cred.APIKey)
}

// maxModelListBytes bounds the /v1/models answer of a key check.
const maxModelListBytes = 4 << 20

// VerifyKey checks a key by listing its models, which only a valid key may
// do. The list is kept as the key's model list.
func (a *Adapter) VerifyKey(ctx context.Context, env provider.Env, client *http.Client, cred credential.Credential) *pluginabi.Error {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, env.BaseURL(cred)+EndpointModels, nil)
	if errReq != nil {
		return env.Errors.Transport(errReq)
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", env.UserAgent)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return env.Errors.Transport(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if errRead != nil {
		return env.Errors.Transport(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return env.Errors.Upstream(resp.StatusCode, resp.Header, body)
	}
	if _, _, errStore := a.catalog.Store(keyID(cred), body, env.Now()); errStore != nil {
		a.logf("warn", "opendesign: model list of the new key unreadable", map[string]any{"error": errStore.Error()})
	}
	return nil
}

// errAccountUnused answers FetchAccount, which the core does not call for a
// QuotaFetcher and QuotaPage.
var errAccountUnused = &pluginabi.Error{Code: "unsupported_operation", Message: "opendesign: API keys report no account", HTTPStatus: http.StatusNotImplemented}

// AccountKey keeps FetchAccount answers out of the core's cache.
func (a *Adapter) AccountKey(provider.Env, credential.Credential) string {
	return ""
}

// FetchAccount is not used; see Adapter.
func (a *Adapter) FetchAccount(provider.Env, *http.Client, credential.Credential) (any, bool, *pluginabi.Error) {
	return nil, false, errAccountUnused
}

// Quota is not used; see Adapter.
func (a *Adapter) Quota(any) pluginapi.QuotaFetchResponse {
	return pluginapi.QuotaFetchResponse{}
}

// DecodeCredentialInput reads nothing: OpenDesign auth files have no
// fields of their own that the credential routes set (the account block is
// written by the account routes).
func (a *Adapter) DecodeCredentialInput([]byte) (any, error) {
	return nil, nil
}

// ApplyCredentialInput has nothing to apply.
func (a *Adapter) ApplyCredentialInput(map[string]any, any) *provider.InputError {
	return nil
}

// CheckCredential has nothing to check besides VerifyKey.
func (a *Adapter) CheckCredential(credential.Credential, *credential.Credential) *provider.CredentialCheck {
	return nil
}

// CredentialView names the key in the models view (key_id) without
// revealing it, counts the chat models its plan lists once its list is
// loaded (listed_models), both before disabled, and ends with the account
// signed in on the card, if any.
func (a *Adapter) CredentialView(cred credential.Credential) []provider.ViewMember {
	id := keyID(cred)
	members := []provider.ViewMember{{Name: "key_id", Value: id, Before: "disabled"}}
	if snapshot := a.catalog.current(); snapshot.KnowsKey(id) {
		members = append(members, provider.ViewMember{Name: "listed_models", Value: snapshot.listedChat(id), Before: "disabled"})
	}
	if link := accountLinkOf(accountOf(cred)); link != nil {
		members = append(members, provider.ViewMember{Name: "account", Value: link})
	}
	return members
}

// ManagementRoutes are the account sign-in routes of the key cards.
func (a *Adapter) ManagementRoutes() []provider.ManagementRoute {
	return []provider.ManagementRoute{
		{Method: http.MethodPost, Path: accountLoginPath, Description: `Start signing an OpenDesign account in on a credential (device authorization, as vela login). Body: {"auth_index"}. Answers the activation link and code.`,
			Handle: func(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
				return a.startAccountLogin(req.Body)
			}},
		{Method: http.MethodGet, Path: accountLoginPath, Description: "State of a sign-in (?id=): pending, approved, denied, expired, cancelled or failed.",
			Handle: func(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
				return a.accountLoginStatus(req.Query.Get("id"))
			}},
		{Method: http.MethodDelete, Path: accountLoginPath, Description: "Cancel a pending sign-in (?id=).",
			Handle: func(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
				return a.cancelAccountLogin(req.Query.Get("id"))
			}},
		{Method: http.MethodDelete, Path: accountPath, Description: "Sign the OpenDesign account out of a credential and revoke its key (?auth_index=).",
			Handle: func(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
				return a.logoutAccount(req.Query.Get("auth_index"))
			}},
	}
}

// ServeQuota serves a card's figures (?auth_index=&refresh=1).
func (a *Adapter) ServeQuota(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	return a.credentialQuota(req.Query.Get("auth_index"), req.Query.Get("refresh") == "1")
}

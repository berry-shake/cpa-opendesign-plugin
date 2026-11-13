package plugin

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/catalog"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/config"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/credential"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/proxystore"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The status and models views of the management page.

type statusView struct {
	Plugin      string           `json:"plugin"`
	Version     string           `json:"version"`
	Now         string           `json:"now"`
	Config      configView       `json:"config"`
	Catalog     catalogView      `json:"catalog"`
	Credentials []credentialView `json:"credentials"`
	ListError   string           `json:"list_error,omitempty"`
}

type configView struct {
	BaseURL       string `json:"base_url"`
	DefaultPrefix string `json:"default_prefix"`
	// ForceModelPrefix mirrors CPA's routing.force-model-prefix: when false
	// CPA registers the bare model ID next to the prefixed one. Omitted until
	// CPA has sent its config summary.
	ForceModelPrefix         *bool  `json:"force_model_prefix,omitempty"`
	ModelsRefreshMinutes     int    `json:"models_refresh_minutes"`
	RequestTimeoutSeconds    int    `json:"request_timeout_seconds"`
	StreamIdleTimeoutSeconds int    `json:"stream_idle_timeout_seconds"`
	GlobalProxy              string `json:"global_proxy,omitempty"`
}

type catalogView struct {
	Count int `json:"count"`
	// Keys counts the keys whose model lists the catalog holds.
	Keys      int    `json:"keys"`
	Revision  string `json:"revision,omitempty"`
	FetchedAt string `json:"fetched_at,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type credentialView struct {
	AuthIndex string `json:"auth_index"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	KeyMasked string `json:"key_masked"`
	// KeyID names the key in the models view (modelView.Keys) without
	// revealing it.
	KeyID string `json:"key_id,omitempty"`
	// ListedModels counts the chat models the key's plan lists; omitted
	// while its list is not loaded.
	ListedModels  *int   `json:"listed_models,omitempty"`
	Disabled      bool   `json:"disabled"`
	Unavailable   bool   `json:"unavailable"`
	Status        string `json:"status,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	Priority      int    `json:"priority"`
	Note          string `json:"note,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	ProxyURL      string `json:"proxy_url,omitempty"`
	// ProxyID and ProxyName name the listed proxy whose URL is proxy_url.
	ProxyID          string   `json:"proxy_id,omitempty"`
	ProxyName        string   `json:"proxy_name,omitempty"`
	Prefix           *string  `json:"prefix"`
	EffectivePrefix  string   `json:"effective_prefix"`
	AllModels        bool     `json:"all_models"`
	Models           []string `json:"models"`
	MissingModels    []string `json:"missing_models,omitempty"`
	RegisteredModels int      `json:"registered_models"`
	RegisteredKnown  bool     `json:"registered_known"`
	Success          int64    `json:"success"`
	Failed           int64    `json:"failed"`
	ReadError        string   `json:"read_error,omitempty"`
}

func (s *Service) status() statusView {
	cfg := s.config()
	view := statusView{
		Plugin:  PluginID,
		Version: s.version,
		Now:     s.now().UTC().Format(time.RFC3339),
		Config: configView{
			BaseURL:                  cfg.BaseURL,
			DefaultPrefix:            cfg.ModelPrefix,
			ModelsRefreshMinutes:     int(cfg.ModelsRefreshInterval / time.Minute),
			RequestTimeoutSeconds:    int(cfg.RequestTimeout / time.Second),
			StreamIdleTimeoutSeconds: int(cfg.StreamIdleTimeout / time.Second),
			GlobalProxy:              redactProxy(s.globalProxy()),
		},
		Catalog:     catalogSummary(s.catalog.Current()),
		Credentials: []credentialView{},
	}
	if force, known := s.forceModelPrefix(); known {
		view.Config.ForceModelPrefix = &force
	}
	entries, errList := s.listOpenDesignAuths()
	if errList != nil {
		view.ListError = errList.Error()
		return view
	}
	registered := s.registeredModels()
	snapshot := s.catalog.Current()
	proxies, _ := s.proxies.List()
	for _, entry := range entries {
		view.Credentials = append(view.Credentials, credentialViewOf(entry, cfg, snapshot, registered, proxies))
	}
	return view
}

func catalogSummary(snapshot catalog.Snapshot) catalogView {
	view := catalogView{Count: len(snapshot.Models), Keys: snapshot.Keys(), Revision: snapshot.Revision, LastError: snapshot.LastError}
	if !snapshot.FetchedAt.IsZero() {
		view.FetchedAt = snapshot.FetchedAt.UTC().Format(time.RFC3339)
	}
	if !snapshot.CheckedAt.IsZero() {
		view.CheckedAt = snapshot.CheckedAt.UTC().Format(time.RFC3339)
	}
	return view
}

func credentialViewOf(entry opendesignAuth, cfg config.Config, snapshot catalog.Snapshot, registered map[string][]string, proxies proxystore.List) credentialView {
	view := credentialView{
		AuthIndex:     entry.AuthIndex,
		ID:            entry.ID,
		Name:          entry.Name,
		Label:         entry.Label,
		Disabled:      entry.Disabled,
		Unavailable:   entry.Unavailable,
		Status:        entry.Status,
		StatusMessage: entry.StatusMessage,
		Priority:      entry.Priority,
		Note:          entry.Note,
		Success:       entry.Success,
		Failed:        entry.Failed,
		Models:        []string{},
		ReadError:     entry.readError,
	}
	if models, ok := registered[entry.AuthIndex]; ok {
		view.RegisteredModels = distinctModels(models, entry.cred.EffectivePrefix(cfg))
		view.RegisteredKnown = true
	} else if registered != nil {
		view.RegisteredKnown = true
	}
	if entry.readError != "" {
		return view
	}
	cred := entry.cred
	// label is the auth file's own label (possibly empty); the page falls
	// back to the masked key for display.
	view.Label = cred.Label
	view.KeyMasked = credential.MaskKey(cred.APIKey)
	view.KeyID = keyID(cred)
	if snapshot.KnowsKey(view.KeyID) {
		listed := 0
		for _, model := range snapshot.Models {
			if model.Chat() && model.ListedBy(view.KeyID) {
				listed++
			}
		}
		view.ListedModels = &listed
	}
	view.BaseURL = cred.BaseURL
	view.ProxyURL = redactProxy(cred.ProxyURL)
	if p, ok := proxies.Match(cred.ProxyURL); ok {
		view.ProxyID, view.ProxyName = p.ID, p.Name
	}
	view.Prefix = cred.Prefix
	view.EffectivePrefix = cred.EffectivePrefix(cfg)
	view.AllModels = cred.AllModels
	if priority := jsonfield.Int(entry.fields, credential.KeyPriority); priority != 0 {
		view.Priority = priority
	}
	if note := jsonfield.String(entry.fields, credential.KeyNote); note != "" {
		view.Note = note
	}
	if !cred.AllModels {
		view.Models = append(view.Models, cred.Models...)
	}
	view.MissingModels = missingModels(cred, snapshot)
	return view
}

// distinctModels counts registered models once each: unless CPA forces the
// prefix, it registers every model both bare and as "<prefix>/<model>".
func distinctModels(ids []string, prefix string) int {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if prefix != "" {
			id = strings.TrimPrefix(id, prefix+"/")
		}
		seen[id] = struct{}{}
	}
	return len(seen)
}

// missingModels lists the selected models the key's plan does not list (or,
// before its list loads, that no key lists). It is empty for all-model
// credentials and while no catalog is loaded.
func missingModels(cred credential.Credential, snapshot catalog.Snapshot) []string {
	if cred.AllModels || snapshot.Empty() {
		return nil
	}
	id, known := keyID(cred), snapshot.KnowsKey(keyID(cred))
	var out []string
	for _, name := range cred.Models {
		model, ok := snapshot.Lookup(name)
		if !ok || known && !model.ListedBy(id) {
			out = append(out, name)
		}
	}
	return out
}

// redactProxy hides proxy passwords for display.
func redactProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.User == nil {
		return raw
	}
	return parsed.Redacted()
}

type modelView struct {
	ID string `json:"id"`
	// Chat is false for models that do not generate text; keys that register
	// all models leave them out.
	Chat    bool     `json:"chat"`
	Enabled bool     `json:"enabled"`
	Default bool     `json:"default,omitempty"`
	Inputs  []string `json:"inputs,omitempty"`
	Outputs []string `json:"outputs,omitempty"`
	// ContextLength is the model's window; ContextBudget and MaxOutput are
	// what OpenDesign lets one request use of it.
	ContextLength int64  `json:"context_length,omitempty"`
	ContextBudget int64  `json:"context_budget,omitempty"`
	MaxOutput     int64  `json:"max_output,omitempty"`
	CostTier      string `json:"cost_tier,omitempty"`
	Capability    string `json:"capability,omitempty"`
	// Keys are the key IDs (credentialView.KeyID) whose plans list the model.
	Keys []string `json:"keys"`
}

type modelsView struct {
	Catalog catalogView `json:"catalog"`
	Models  []modelView `json:"models"`
}

func modelsViewOf(snapshot catalog.Snapshot) modelsView {
	view := modelsView{Catalog: catalogSummary(snapshot), Models: make([]modelView, 0, len(snapshot.Models))}
	for _, model := range snapshot.Models {
		item := modelView{
			ID:            model.ID,
			Chat:          model.Chat(),
			Enabled:       model.Enabled,
			Default:       model.Default,
			Inputs:        model.Inputs,
			Outputs:       model.Outputs,
			ContextLength: model.ContextLength,
			ContextBudget: model.ContextBudget,
			MaxOutput:     model.MaxOutput,
			CostTier:      model.CostTier,
			Capability:    model.Capability,
			Keys:          append([]string{}, model.Keys...),
		}
		view.Models = append(view.Models, item)
	}
	return view
}

func (s *Service) models() pluginapi.ManagementResponse {
	snapshot := s.catalog.Current()
	if snapshot.Empty() {
		var errRefresh error
		snapshot, errRefresh = s.refreshCatalog(false)
		if errRefresh != nil && snapshot.Empty() {
			return jsonResponse(http.StatusBadGateway, errorBody{Error: "catalog_unavailable", Message: errRefresh.Error()})
		}
	}
	return jsonResponse(http.StatusOK, modelsViewOf(snapshot))
}

func (s *Service) refreshModels() pluginapi.ManagementResponse {
	snapshot, errRefresh := s.refreshCatalog(true)
	if errRefresh != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "catalog_unavailable", Message: errRefresh.Error()})
	}
	return jsonResponse(http.StatusOK, modelsViewOf(snapshot))
}

package opendesign

// The catalog caches the OpenDesign model lists, one per key, and turns them
// into CPA model registrations (provider.KeyedCatalogSource).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// maxCatalogBytes bounds the /v1/models response (a Go key's list is ~4 KB).
const maxCatalogBytes = 4 << 20

// Model is one model from GET /v1/models.
type Model struct {
	// ID is the model ID requests use, such as "deepseek-v4.1-flash".
	ID      string
	Created int64
	OwnedBy string
	// Inputs and Outputs are the accepted and produced kinds: text, image,
	// file, audio, video.
	Inputs  []string
	Outputs []string
	// ContextLength is the model's context window; ContextBudget is how much
	// of it OpenDesign lets a request use, MaxOutput the output budget.
	ContextLength int64
	ContextBudget int64
	MaxOutput     int64
	// CostTier ("low", "medium", "high") and Capability ("standard", ...)
	// are OpenDesign's own labels.
	CostTier   string
	Capability string
	Default    bool
	Enabled    bool
	// Keys are the keys (by keyID) whose plan lists the model.
	Keys []string
}

// Chat reports whether the model generates text and is enabled for the
// plan. Others are left out when a key registers all models.
func (m Model) Chat() bool {
	return m.Enabled && (len(m.Outputs) == 0 || slices.Contains(m.Outputs, "text"))
}

// ListedBy reports whether a key's model list has the model.
func (m Model) ListedBy(keyID string) bool {
	return slices.Contains(m.Keys, keyID)
}

type modelsResponse struct {
	Data []struct {
		ID           string `json:"id"`
		Created      int64  `json:"created"`
		OwnedBy      string `json:"owned_by"`
		Architecture struct {
			Input  []string `json:"input_modalities"`
			Output []string `json:"output_modalities"`
		} `json:"architecture"`
		Metadata struct {
			Cost          string `json:"cost"`
			Capability    string `json:"capability"`
			ContextLimit  int64  `json:"context_limit"`
			ContextBudget int64  `json:"context_budget"`
			OutputBudget  int64  `json:"output_budget"`
		} `json:"metadata"`
		Default bool  `json:"default"`
		Enabled *bool `json:"enabled"`
	} `json:"data"`
}

// keyList is the model list one key's plan reported.
type keyList struct {
	models []Model
	at     time.Time
}

// Snapshot is an immutable view of the cached catalog: the union of the
// keys' model lists.
type Snapshot struct {
	Models    []Model
	byID      map[string]Model
	keys      map[string]time.Time
	Revision  string
	FetchedAt time.Time
	CheckedAt time.Time
	LastError string
}

// Empty reports whether no catalog has been loaded.
func (c Snapshot) Empty() bool { return len(c.Models) == 0 }

// Lookup finds a model by its ID.
func (c Snapshot) Lookup(id string) (Model, bool) {
	model, ok := c.byID[id]
	return model, ok
}

// KnowsKey reports whether the catalog has a key's model list.
func (c Snapshot) KnowsKey(keyID string) bool {
	_, ok := c.keys[keyID]
	return ok
}

// Keys counts the keys whose model lists the catalog holds.
func (c Snapshot) Keys() int { return len(c.keys) }

// Summary describes the snapshot on the management page.
func (c Snapshot) Summary() provider.CatalogSummary {
	return provider.CatalogSummary{Count: len(c.Models), Revision: c.Revision, FetchedAt: c.FetchedAt, CheckedAt: c.CheckedAt, LastError: c.LastError}
}

// Has reports whether a model is in the catalog (any key lists it).
func (c Snapshot) Has(id string) bool {
	_, ok := c.Lookup(id)
	return ok
}

// KeyHas reports whether a key may register a model: the catalog has it and,
// once the key's list is loaded, the key's plan lists it.
func (c Snapshot) KeyHas(keyID, id string) bool {
	model, ok := c.Lookup(id)
	return ok && (!c.KnowsKey(keyID) || model.ListedBy(keyID))
}

// ModelInfos is KeyModelInfos for no particular key.
func (c Snapshot) ModelInfos(all bool, ids []string) []pluginapi.ModelInfo {
	return c.KeyModelInfos(all, ids, "")
}

// listedChat counts the chat models a key's plan lists.
func (c Snapshot) listedChat(keyID string) int {
	listed := 0
	for _, model := range c.Models {
		if model.Chat() && model.ListedBy(keyID) {
			listed++
		}
	}
	return listed
}

// modelView is one model of the management page's models view.
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
	// Keys are the key IDs (the credential view's key_id) whose plans list
	// the model.
	Keys []string `json:"keys"`
}

// ModelsView lists the models for the management page.
func (c Snapshot) ModelsView() any {
	out := make([]modelView, 0, len(c.Models))
	for _, model := range c.Models {
		out = append(out, modelView{
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
		})
	}
	return out
}

// Catalog caches the model list of every key; a plan decides which models a
// key may use, so keys of different plans list different models. The
// endpoint sends no ETag, so every refresh reloads a list.
type Catalog struct {
	mu       sync.RWMutex
	snapshot Snapshot
	lists    map[string]keyList
	// errs are the errors of the keys whose last load failed, by keyID; the
	// snapshot's LastError lists them all.
	errs map[string]string
	// fetchMu serializes upstream fetches so concurrent model.for_auth calls
	// during startup share one request per key.
	fetchMu sync.Mutex
}

var (
	_ provider.KeyedCatalogSource = (*Catalog)(nil)
	_ provider.KeyedCatalog       = Snapshot{}
)

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{snapshot: Snapshot{byID: map[string]Model{}, keys: map[string]time.Time{}}, lists: map[string]keyList{}, errs: map[string]string{}}
}

// Current returns the cached snapshot.
func (c *Catalog) Current() provider.Catalog {
	return c.current()
}

func (c *Catalog) current() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// errNoKey answers Fetch: the lists are loaded per key (FetchKey).
var errNoKey = errors.New("opendesign: model lists are loaded per key")

// Fetch is not used: the core loads a keyed catalog with FetchKey.
func (c *Catalog) Fetch(context.Context, *http.Client, string, string, bool, time.Time) (provider.Catalog, error) {
	return c.current(), errNoKey
}

// FetchKey loads the model list of one key through client.
func (c *Catalog) FetchKey(ctx context.Context, client *http.Client, baseURL, apiKey, keyID, userAgent string, now time.Time) (provider.Catalog, error) {
	snapshot, _, errFetch := c.fetch(ctx, client, baseURL, apiKey, keyID, userAgent, now)
	return snapshot, errFetch
}

// Retain drops the lists of keys outside keep (deleted credentials).
func (c *Catalog) Retain(keep map[string]bool) provider.Catalog {
	return c.retain(keep)
}

// fetch loads the model list of one key (keyID names it without revealing
// it) through client. It returns the new snapshot and whether the registered
// model set changed.
func (c *Catalog) fetch(ctx context.Context, client *http.Client, baseURL, apiKey, keyID, userAgent string, now time.Time) (Snapshot, bool, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+EndpointModels, nil)
	if errReq != nil {
		return c.current(), false, errReq
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return c.recordError(keyID, now, errDo)
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	_ = resp.Body.Close()
	if errRead != nil {
		return c.recordError(keyID, now, errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return c.recordError(keyID, now, fmt.Errorf("GET %s returned %d: %s", EndpointModels, resp.StatusCode, truncate(string(body), 300)))
	}
	return c.Store(keyID, body, now)
}

// Store merges a /v1/models response listed by one key into the catalog.
func (c *Catalog) Store(keyID string, body []byte, now time.Time) (Snapshot, bool, error) {
	models, errParse := ParseModels(body)
	if errParse != nil {
		return c.recordError(keyID, now, errParse)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.snapshot.Revision
	c.lists[keyID] = keyList{models: models, at: now}
	delete(c.errs, keyID)
	c.rebuildLocked(now)
	return c.snapshot, c.snapshot.Revision != previous, nil
}

// retain drops the lists of keys outside keep (deleted credentials).
func (c *Catalog) retain(keep map[string]bool) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := false
	for keyID := range c.lists {
		if !keep[keyID] {
			delete(c.lists, keyID)
			removed = true
		}
	}
	for keyID := range c.errs {
		if !keep[keyID] {
			delete(c.errs, keyID)
			removed = true
		}
	}
	if removed {
		c.rebuildLocked(c.snapshot.FetchedAt)
	}
	return c.snapshot
}

// rebuildLocked recomputes the union snapshot from the key lists.
func (c *Catalog) rebuildLocked(fetchedAt time.Time) {
	byID := map[string]Model{}
	keys := make(map[string]time.Time, len(c.lists))
	keyIDs := make([]string, 0, len(c.lists))
	for keyID := range c.lists {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		list := c.lists[keyID]
		keys[keyID] = list.at
		for _, model := range list.models {
			merged, seen := byID[model.ID]
			if !seen {
				merged = model
				merged.Keys = nil
			}
			merged.Keys = append(merged.Keys, keyID)
			byID[model.ID] = merged
		}
	}
	models := make([]Model, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID) })
	c.snapshot = Snapshot{
		Models:    models,
		byID:      byID,
		keys:      keys,
		Revision:  catalogRevision(models),
		FetchedAt: fetchedAt,
		CheckedAt: fetchedAt,
		LastError: c.lastErrorLocked(),
	}
}

// lastErrorLocked lists the distinct errors of the keys whose last load
// failed, in key order.
func (c *Catalog) lastErrorLocked() string {
	keyIDs := make([]string, 0, len(c.errs))
	for keyID := range c.errs {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	var messages []string
	for _, keyID := range keyIDs {
		if message := c.errs[keyID]; !slices.Contains(messages, message) {
			messages = append(messages, message)
		}
	}
	return strings.Join(messages, "; ")
}

// ParseModels reads a GET /v1/models response.
func ParseModels(body []byte) ([]Model, error) {
	var list modelsResponse
	if errUnmarshal := json.Unmarshal(body, &list); errUnmarshal != nil {
		return nil, fmt.Errorf("decode %s: %w", EndpointModels, errUnmarshal)
	}
	models := make([]Model, 0, len(list.Data))
	seen := make(map[string]struct{}, len(list.Data))
	for _, item := range list.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, Model{
			ID:            id,
			Created:       item.Created,
			OwnedBy:       strings.TrimSpace(item.OwnedBy),
			Inputs:        kinds(item.Architecture.Input, "text"),
			Outputs:       kinds(item.Architecture.Output, "text"),
			ContextLength: item.Metadata.ContextLimit,
			ContextBudget: item.Metadata.ContextBudget,
			MaxOutput:     item.Metadata.OutputBudget,
			CostTier:      strings.ToLower(strings.TrimSpace(item.Metadata.Cost)),
			Capability:    strings.ToLower(strings.TrimSpace(item.Metadata.Capability)),
			Default:       item.Default,
			Enabled:       item.Enabled == nil || *item.Enabled,
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%s returned no models", EndpointModels)
	}
	return models, nil
}

// kinds normalizes modality names (text, image, file, audio, video), text
// first; fallback is used when the list is empty.
func kinds(raw []string, fallback string) []string {
	have := map[string]bool{}
	for _, kind := range raw {
		kind = strings.ToLower(strings.TrimSpace(kind))
		switch {
		case kind == "text" || strings.HasPrefix(kind, "text/"):
			have["text"] = true
		case kind == "image" || strings.HasPrefix(kind, "image/"):
			have["image"] = true
		case kind == "file" || kind == "pdf" || kind == "application/pdf":
			have["file"] = true
		case kind == "audio" || strings.HasPrefix(kind, "audio/"):
			have["audio"] = true
		case kind == "video" || strings.HasPrefix(kind, "video/"):
			have["video"] = true
		}
	}
	var out []string
	for _, kind := range []string{"text", "image", "file", "audio", "video"} {
		if have[kind] {
			out = append(out, kind)
		}
	}
	if len(out) == 0 && fallback != "" {
		out = []string{fallback}
	}
	return out
}

func (c *Catalog) recordError(keyID string, now time.Time, err error) (Snapshot, bool, error) {
	c.mu.Lock()
	c.errs[keyID] = err.Error()
	c.snapshot.CheckedAt = now
	c.snapshot.LastError = c.lastErrorLocked()
	snapshot := c.snapshot
	c.mu.Unlock()
	return snapshot, false, err
}

// catalogRevision hashes the parts of the models that end up in CPA's model
// registry, so only changes there trigger re-registration.
func catalogRevision(models []Model) string {
	hash := sha256.New()
	for _, model := range models {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%d\x00%s\x00%t\n",
			model.ID, model.ContextLength, model.MaxOutput, model.Created,
			strings.Join(model.Inputs, ","), model.Chat())
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// RevisionFor is the revision of what one key registers: the models its plan
// lists, or the whole catalog while its list is not loaded. A key added or
// dropped elsewhere leaves it unchanged, so other keys are not re-registered.
// It is empty while the catalog is empty.
func (c Snapshot) RevisionFor(keyID string) string {
	if c.Empty() {
		return ""
	}
	if !c.KnowsKey(keyID) {
		return c.Revision
	}
	listed := make([]Model, 0, len(c.Models))
	for _, model := range c.Models {
		if model.ListedBy(keyID) {
			listed = append(listed, model)
		}
	}
	return catalogRevision(listed)
}

// KeyModelInfos builds the CPA model list for one credential: every chat
// model its key's list has when all is set (every chat model of the catalog
// while the key's list is not loaded), otherwise the selected IDs. Selected
// IDs missing from the catalog are still registered (with minimal metadata)
// so a catalog outage or a retired model does not silently drop routes the
// user picked. IDs are the bare upstream IDs; CPA adds the auth's prefix
// itself.
func (c Snapshot) KeyModelInfos(all bool, ids []string, keyID string) []pluginapi.ModelInfo {
	var selected []Model
	if all {
		known := c.KnowsKey(keyID)
		selected = make([]Model, 0, len(c.Models))
		for _, model := range c.Models {
			if model.Chat() && (!known || model.ListedBy(keyID)) {
				selected = append(selected, model)
			}
		}
	} else {
		selected = make([]Model, 0, len(ids))
		for _, id := range ids {
			if model, ok := c.Lookup(id); ok {
				selected = append(selected, model)
				continue
			}
			selected = append(selected, Model{ID: id})
		}
	}
	out := make([]pluginapi.ModelInfo, 0, len(selected))
	for _, model := range selected {
		out = append(out, toModelInfo(model))
	}
	return out
}

func toModelInfo(model Model) pluginapi.ModelInfo {
	inputs := model.Inputs
	if len(inputs) == 0 {
		inputs = []string{"text"}
	}
	return pluginapi.ModelInfo{
		ID:                        model.ID,
		Object:                    "model",
		Created:                   model.Created,
		OwnedBy:                   ProviderID,
		Type:                      ProviderID,
		DisplayName:               model.ID,
		Name:                      model.ID,
		Description:               "OpenDesign · " + model.ID,
		InputTokenLimit:           model.ContextLength,
		OutputTokenLimit:          model.MaxOutput,
		ContextLength:             model.ContextLength,
		MaxCompletionTokens:       model.MaxOutput,
		SupportedInputModalities:  append([]string(nil), inputs...),
		SupportedOutputModalities: []string{"text"},
		// UserDefined keeps CPA from validating or stripping reasoning
		// settings; the list does not say which models take them.
		UserDefined: true,
	}
}

// UpstreamID maps the model CPA hands the executor back to OpenDesign's ID
// (provider.UpstreamID).
func (c Snapshot) UpstreamID(model, prefix string) string {
	return provider.UpstreamID(model, prefix, c.Has, provider.SlashedIDs)
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

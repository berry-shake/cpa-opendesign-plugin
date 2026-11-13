// Package catalog caches the OpenDesign model lists and turns them into CPA
// model registrations.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/opendesign"
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

// Catalog caches the model list of every key; a plan decides which models a
// key may use, so keys of different plans list different models. The
// endpoint sends no ETag, so every refresh reloads a list.
type Catalog struct {
	mu       sync.RWMutex
	snapshot Snapshot
	lists    map[string]keyList
	// fetchMu serializes upstream fetches so concurrent model.for_auth calls
	// during startup share one request per key.
	fetchMu sync.Mutex
}

// New returns an empty catalog.
func New() *Catalog {
	return &Catalog{snapshot: Snapshot{byID: map[string]Model{}, keys: map[string]time.Time{}}, lists: map[string]keyList{}}
}

// Current returns the cached snapshot.
func (c *Catalog) Current() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// Fetch loads the model list of one key (keyID names it without revealing
// it) through client. It returns the new snapshot and whether the registered
// model set changed.
func (c *Catalog) Fetch(ctx context.Context, client *http.Client, baseURL, apiKey, keyID, userAgent string, now time.Time) (Snapshot, bool, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+opendesign.EndpointModels, nil)
	if errReq != nil {
		return c.Current(), false, errReq
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return c.recordError(now, errDo)
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	_ = resp.Body.Close()
	if errRead != nil {
		return c.recordError(now, errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return c.recordError(now, fmt.Errorf("GET %s returned %d: %s", opendesign.EndpointModels, resp.StatusCode, truncate(string(body), 300)))
	}
	return c.Store(keyID, body, now)
}

// Store merges a /v1/models response listed by one key into the catalog.
func (c *Catalog) Store(keyID string, body []byte, now time.Time) (Snapshot, bool, error) {
	models, errParse := ParseModels(body)
	if errParse != nil {
		return c.recordError(now, errParse)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.snapshot.Revision
	c.lists[keyID] = keyList{models: models, at: now}
	c.rebuildLocked(now)
	return c.snapshot, c.snapshot.Revision != previous, nil
}

// Retain drops the lists of keys outside keep (deleted credentials).
func (c *Catalog) Retain(keep map[string]bool) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := false
	for keyID := range c.lists {
		if !keep[keyID] {
			delete(c.lists, keyID)
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
	}
}

// ParseModels reads a GET /v1/models response.
func ParseModels(body []byte) ([]Model, error) {
	var list modelsResponse
	if errUnmarshal := json.Unmarshal(body, &list); errUnmarshal != nil {
		return nil, fmt.Errorf("decode %s: %w", opendesign.EndpointModels, errUnmarshal)
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
		return nil, fmt.Errorf("%s returned no models", opendesign.EndpointModels)
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

func (c *Catalog) recordError(now time.Time, err error) (Snapshot, bool, error) {
	c.mu.Lock()
	c.snapshot.CheckedAt = now
	c.snapshot.LastError = err.Error()
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

// ModelInfos builds the CPA model list for one credential: every chat model
// its key's list has when all is set (every chat model of the catalog while
// the key's list is not loaded), otherwise the selected IDs. Selected IDs
// missing from the catalog are still registered (with minimal metadata) so a
// catalog outage or a retired model does not silently drop routes the user
// picked. IDs are the bare upstream IDs; CPA adds the auth's prefix itself.
func (c Snapshot) ModelInfos(all bool, ids []string, keyID string) []pluginapi.ModelInfo {
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
		OwnedBy:                   opendesign.ProviderID,
		Type:                      opendesign.ProviderID,
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

// UpstreamID maps the model CPA hands the executor back to OpenDesign's ID.
// CPA strips the auth prefix before calling the executor; the prefix is
// still removed here in case a model arrives with it.
func (c Snapshot) UpstreamID(model, prefix string) string {
	model = stripThinkingSuffix(strings.TrimSpace(model))
	if prefix != "" && strings.HasPrefix(model, prefix+"/") {
		return strings.TrimPrefix(model, prefix+"/")
	}
	if _, ok := c.Lookup(model); ok {
		return model
	}
	// A prefix we do not know (for example from a changed config) is dropped
	// when what follows it is a catalog model.
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		if _, ok := c.Lookup(model[idx+1:]); ok {
			return model[idx+1:]
		}
	}
	return model
}

// stripThinkingSuffix removes a trailing CPA reasoning suffix such as
// "(high)" or "(8192)". CPA normally strips it before calling the executor.
func stripThinkingSuffix(model string) string {
	if !strings.HasSuffix(model, ")") {
		return model
	}
	open := strings.LastIndex(model, "(")
	if open <= 0 {
		return model
	}
	return strings.TrimSpace(model[:open])
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

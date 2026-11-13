package plugin

import (
	"context"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/catalog"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/credential"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/opendesign"
)

// loadCatalogFor loads a credential's model list synchronously through its
// own egress, unless the catalog already has it.
func (s *Service) loadCatalogFor(cred credential.Credential) catalog.Snapshot {
	if snapshot := s.catalog.Current(); snapshot.KnowsKey(keyID(cred)) || cred.APIKey == "" {
		return snapshot
	}
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	setting, errProxy := opendesign.ResolveEgress(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return s.catalog.Current()
	}
	snapshot, _, errFetch := s.catalog.Fetch(ctx, s.clients.Client(setting), cred.EffectiveBaseURL(s.config()), cred.APIKey, keyID(cred), s.userAgent(), s.now())
	if errFetch != nil {
		s.logf("warn", "opendesign: model list unavailable", map[string]any{"error": errFetch.Error()})
	}
	return snapshot
}

func (s *Service) kickRefresh() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// refreshLoop polls the catalog and pushes changes to CPA.
func (s *Service) refreshLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.kick:
		case <-timer.C:
		}
		s.refreshCatalog(false)
		if interval := s.config().ModelsRefreshInterval; interval > 0 {
			timer.Reset(interval)
		}
	}
}

// refreshCatalog reloads the model list of every key (each through its own
// egress), drops the lists of deleted keys and re-registers credentials whose
// models were built from an older catalog. It fails only when no list loads.
func (s *Service) refreshCatalog(force bool) (catalog.Snapshot, error) {
	_ = force // every refresh reloads the lists in full
	entries, _ := s.listOpenDesignAuths()
	keep := map[string]bool{}
	var (
		loaded  int
		lastErr error
	)
	for _, entry := range entries {
		if entry.readError != "" || entry.cred.APIKey == "" || keep[keyID(entry.cred)] {
			continue
		}
		keep[keyID(entry.cred)] = true
		setting, errProxy := opendesign.ResolveEgress(entry.cred.ProxyURL, s.globalProxy())
		if errProxy != nil {
			lastErr = errProxy
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
		_, _, errFetch := s.catalog.Fetch(ctx, s.clients.Client(setting), entry.cred.EffectiveBaseURL(s.config()), entry.cred.APIKey, keyID(entry.cred), s.userAgent(), s.now())
		cancel()
		if errFetch != nil {
			lastErr = errFetch
			continue
		}
		loaded++
	}
	snapshot := s.catalog.Current()
	if len(entries) > 0 || snapshot.Keys() > 0 {
		snapshot = s.catalog.Retain(keep)
	}
	if loaded == 0 && lastErr != nil {
		s.logf("warn", "opendesign: model list refresh failed", map[string]any{"error": lastErr.Error()})
		return snapshot, lastErr
	}
	if lastErr != nil {
		s.logf("warn", "opendesign: some model lists did not load", map[string]any{"error": lastErr.Error()})
	}
	s.syncRegistrations(snapshot, entries)
	return snapshot, nil
}

// syncRegistrations re-saves auth files whose registered models predate their
// key's current model list. Saving makes CPA re-parse the file and call
// model.for_auth again. model_revision records the revision (RevisionFor) a
// file was last synced with; the in-memory registered map catches enabled
// credentials whose models were built from an older list. Disabled credentials are not registered by CPA, so
// only their file revision is checked and they are touched once per change.
func (s *Service) syncRegistrations(snapshot catalog.Snapshot, entries []opendesignAuth) {
	for _, entry := range entries {
		if entry.readError != "" || !credential.IsOpenDesign(entry.fields) {
			continue
		}
		revision := snapshot.RevisionFor(keyID(entry.cred))
		if revision == "" {
			continue
		}
		stale := entry.cred.ModelRevision != revision
		if !entry.Disabled {
			if registered, known := s.registered.Load(entry.ID); known && registered != revision {
				stale = true
			}
		}
		if !stale {
			continue
		}
		name := credential.SaveName(entry.Name, entry.Path)
		if name == "" {
			continue
		}
		fields := cloneFields(entry.fields)
		fields[credential.KeyModelRevision] = revision
		if errSave := s.saveAuthFile(name, fields); errSave != nil {
			s.logf("warn", "opendesign: model sync save failed", map[string]any{"auth": name, "error": errSave.Error()})
			continue
		}
		s.logf("info", "opendesign: refreshed registered models", map[string]any{"auth": name, "revision": revision})
	}
}

func cloneFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		out[key] = value
	}
	return out
}

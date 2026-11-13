package plugin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/berry-shake/cpa-opendesign-plugin/internal/credential"
	"github.com/berry-shake/cpa-opendesign-plugin/internal/opendesign"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// accountCallTimeout bounds one key check or model list load.
const accountCallTimeout = 20 * time.Second

// OpenDesign key checks and model list loads go out one at a time per
// egress: a call waits for the one ahead of it on the same proxy, or the
// same direct line, to finish. Keys on different egresses are queried in
// parallel.

// accountTurn resolves the egress for a credential's proxy and waits for
// its turn, giving up when ctx ends. release must be called when done.
func (s *Service) accountTurn(ctx context.Context, proxyURL string) (client *http.Client, release func(), perr *pluginabi.Error) {
	setting, errProxy := opendesign.ResolveEgress(proxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, nil, opendesign.RequestError("invalid_proxy", errProxy.Error())
	}
	release, errWait := s.accountQueue.Acquire(ctx, setting)
	if errWait != nil {
		return nil, nil, opendesign.TransportError(fmt.Errorf("waiting for the egress: %w", errWait))
	}
	return s.clients.Client(setting), release, nil
}

// keyID names a key in the catalog without revealing it.
func keyID(cred credential.Credential) string {
	return credential.Fingerprint(cred.APIKey)
}

// checkKey verifies a key by listing its models, which only a valid key may
// do, in its egress's turn. The list is kept as the key's model list.
func (s *Service) checkKey(ctx context.Context, cred credential.Credential) *pluginabi.Error {
	client, release, perr := s.accountTurn(ctx, cred.ProxyURL)
	if perr != nil {
		return perr
	}
	defer release()
	body, perr := opendesign.Get(ctx, client, cred.EffectiveBaseURL(s.config())+opendesign.EndpointModels, cred.APIKey, s.userAgent(), opendesign.EndpointModels)
	if perr != nil {
		return perr
	}
	if _, _, errStore := s.catalog.Store(keyID(cred), body, s.now()); errStore != nil {
		s.logf("warn", "opendesign: model list of the new key unreadable", map[string]any{"error": errStore.Error()})
	}
	return nil
}

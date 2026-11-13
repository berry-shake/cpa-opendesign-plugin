package opendesign

import (
	"context"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// Get GETs an OpenDesign endpoint with the key as a bearer token and returns
// the body of a 200 answer. name labels the endpoint in errors.
func Get(ctx context.Context, client *http.Client, target, token, userAgent, name string) ([]byte, *pluginabi.Error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if errReq != nil {
		return nil, TransportError(errReq)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, TransportError(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return nil, TransportError(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, UpstreamError(resp.StatusCode, resp.Header, body, name)
	}
	return body, nil
}

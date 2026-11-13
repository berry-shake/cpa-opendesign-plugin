// Package opendesign holds what the plugin knows about the OpenDesign Go API
// itself: endpoints, error mapping and egress (proxies and HTTP clients).
package opendesign

import "strings"

// ProviderID is the CPA provider key and the auth file type.
const ProviderID = "opendesign"

// Endpoints, relative to the base URL https://amr-link.open-design.ai. Every
// model answers on all three: the gateway translates a request for a model
// whose native API is another one.
const (
	EndpointChat      = "/v1/chat/completions"
	EndpointResponses = "/v1/responses"
	EndpointMessages  = "/v1/messages"
	// EndpointModels lists the models the key's plan may use; it needs the
	// key.
	EndpointModels = "/v1/models"
)

// EndpointForFormat maps a CPA executor format to the OpenDesign endpoint.
func EndpointForFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions":
		return EndpointChat, true
	case "openai-response", "openai-responses", "responses":
		return EndpointResponses, true
	case "claude", "anthropic":
		return EndpointMessages, true
	default:
		return "", false
	}
}

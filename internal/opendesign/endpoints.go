// Package opendesign is the OpenDesign provider for cpa-provider-core: what
// the plugin knows about the OpenDesign Go API itself (endpoints, the model
// lists of each key, the stream repairs, the errors it words its own way)
// and the OpenDesign account a key card can sign in to read the plan's
// windows (vela device authorization).
package opendesign

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

package opendesign

import "strings"

// RemapError is OpenDesign's rule on top of the core's error mapping
// (upstream.Errors): its OpenAI-style errors repeat the code in front of the
// message ("[code=model_not_found] The requested model was not found."), so
// that prefix is dropped. Statuses map the core's way.
func (a *Adapter) RemapError(status int, code, message string) (int, string) {
	if strings.HasPrefix(message, "[code=") {
		if end := strings.Index(message, "] "); end > 0 {
			message = strings.TrimSpace(message[end+2:])
		}
	}
	return 0, message
}

package opendesign

import (
	"bytes"
	"encoding/json"

	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/berry-shake/cpa-provider-core/payload"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/streamgate"
)

// StreamFix repairs the Messages streams OpenDesign gets wrong (see
// messagesFix); the other protocols stream as they are. max_tokens, read
// from the request when its scan found no malformed JSON, lets a stream cut
// at the limit end normally.
func (a *Adapter) StreamFix(protocol provider.Protocol, in provider.CallInput) provider.StreamFix {
	if protocol != provider.Messages {
		return nil
	}
	fix := &messagesFix{}
	if in.ScanErr == nil {
		if value, ok := payload.Value(in.Request.Payload, in.Members, "max_tokens"); ok {
			if number, isNumber := value.(float64); isNumber && number > 0 {
				fix.maxTokens = int64(number)
			}
		}
	}
	return fix
}

// messagesFix repairs the Messages events OpenDesign gets wrong before they
// go through the stream gate.
//
// OpenDesign starts a thinking block as redacted_thinking and then streams
// thinking_delta and signature_delta events into it (seen 2026-11-14; the
// non-streaming response has a thinking block). A client that follows the
// protocol rejects a signature for a block that is not thinking: Claude Code
// drops the stream and asks again without streaming. The start is held back
// until the next event: thinking text or a signature turn it into the
// thinking block it is; anything else lets it through as sent.
//
// OpenDesign also ends a streamed response that calls a tool with
// stop_reason end_turn, where its non-streaming response says tool_use; the
// stop reason of a stream that started a tool_use block is set to tool_use.
//
// When a response stops at max_tokens, OpenDesign's Messages stream ends
// right after the last content_block_stop, with no message_delta and no
// message_stop (seen 2026-11-13; a response that ends on its own carries
// both). The stream has delivered everything, so Close sends the missing
// events with stop_reason "max_tokens" and output_tokens set to the limit it
// stopped at, instead of the gate failing the request as truncated.
type messagesFix struct {
	// maxTokens is the request's max_tokens, 0 when absent.
	maxTokens int64
	// blocks counts the content blocks passed on and not yet stopped.
	blocks int
	// pendingStart is a redacted_thinking block start held until the next
	// event shows what the block carries; pendingIndex is its index.
	pendingStart []byte
	pendingIndex float64
	// toolUse is set once a tool_use block started.
	toolUse bool
}

func (f *messagesFix) Event(event []byte) [][]byte {
	var out [][]byte
	ev := streamgate.NewEvent(event)
	if held := f.pendingStart; held != nil {
		f.pendingStart = nil
		if carriesThinking(ev, f.pendingIndex) {
			held = thinkingBlockStart(held)
		}
		out = append(out, f.pass(held, streamgate.NewEvent(held)))
	}
	switch ev.Text("type") {
	case "content_block_start":
		eventPayload := ev.Payload()
		block, _ := eventPayload["content_block"].(map[string]any)
		switch jsonfield.Text(block["type"]) {
		case "redacted_thinking":
			if index, ok := eventPayload["index"].(float64); ok {
				f.pendingStart, f.pendingIndex = bytes.Clone(event), index
				return out
			}
		case "tool_use", "server_tool_use":
			f.toolUse = true
		}
	case "message_delta":
		if f.toolUse {
			if fixed := toolUseStop(ev); fixed != nil {
				event, ev = fixed, streamgate.NewEvent(fixed)
			}
		}
	}
	return append(out, f.pass(event, ev))
}

// Flush passes on a block start held at the end of the stream as it came;
// the open block leaves the stream truncated.
func (f *messagesFix) Flush() [][]byte {
	held := f.pendingStart
	if held == nil {
		return nil
	}
	f.pendingStart = nil
	return [][]byte{f.pass(held, streamgate.NewEvent(held))}
}

// Close ends a stream cut at max_tokens (no block open, max_tokens known).
func (f *messagesFix) Close() [][]byte {
	if f.blocks != 0 || f.maxTokens <= 0 {
		return nil
	}
	delta, _ := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "max_tokens", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": f.maxTokens},
	})
	return [][]byte{
		append(append([]byte("event: message_delta\ndata: "), delta...), "\n\n"...),
		[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
	}
}

// pass counts the content blocks of an event passed on to the gate.
func (f *messagesFix) pass(event []byte, ev *streamgate.Event) []byte {
	switch ev.Text("type") {
	case "content_block_start":
		f.blocks++
	case "content_block_stop":
		f.blocks--
	}
	return event
}

// carriesThinking reports whether ev streams thinking text or a signature
// into the block at index.
func carriesThinking(ev *streamgate.Event, index float64) bool {
	if ev.Text("type") != "content_block_delta" {
		return false
	}
	eventPayload := ev.Payload()
	if at, ok := eventPayload["index"].(float64); !ok || at != index {
		return false
	}
	delta, _ := eventPayload["delta"].(map[string]any)
	switch jsonfield.Text(delta["type"]) {
	case "signature_delta":
		return true
	case "thinking_delta":
		_, text := delta["thinking"].(string)
		return text
	}
	return false
}

// thinkingBlockStart turns a redacted_thinking block start into a thinking
// one with empty thinking and signature, which the deltas fill in.
func thinkingBlockStart(raw []byte) []byte {
	ev := streamgate.NewEvent(raw)
	eventPayload := ev.Payload()
	block, _ := eventPayload["content_block"].(map[string]any)
	if block == nil {
		return raw
	}
	eventPayload["content_block"] = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	return sseEvent(ev.Name(), eventPayload, raw)
}

// toolUseStop returns ev with stop_reason tool_use when it ends the message
// with end_turn, or nil.
func toolUseStop(ev *streamgate.Event) []byte {
	eventPayload := ev.Payload()
	delta, _ := eventPayload["delta"].(map[string]any)
	if delta == nil || jsonfield.Text(delta["stop_reason"]) != "end_turn" {
		return nil
	}
	delta["stop_reason"] = "tool_use"
	return sseEvent(ev.Name(), eventPayload, nil)
}

// sseEvent writes an event with the given name and data; fallback is
// returned when the data cannot be encoded.
func sseEvent(name string, eventPayload map[string]any, fallback []byte) []byte {
	data, errMarshal := json.Marshal(eventPayload)
	if errMarshal != nil {
		return fallback
	}
	var out []byte
	if name != "" {
		out = append(append(append(out, "event: "...), name...), '\n')
	}
	return append(append(append(out, "data: "...), data...), "\n\n"...)
}

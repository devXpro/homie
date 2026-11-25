package audit

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// sensitiveKey matches argument names whose values never reach the activity log (docs/SECURITY.md).
var sensitiveKey = regexp.MustCompile(`(?i)(token|key|secret|password|pin|code)`)

const redacted = "[redacted]"

// Redact returns args with the value of every object key matching sensitiveKey replaced by "[redacted]", at any depth.
// Empty input returns nil; input that is not JSON is replaced as a whole, since it cannot be inspected.
func Redact(args json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return json.RawMessage(`"[unreadable]"`)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactValue(v)); err != nil {
		return json.RawMessage(`"[unreadable]"`)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func redactValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if sensitiveKey.MatchString(k) {
				v[k] = redacted
			} else {
				v[k] = redactValue(child)
			}
		}
		return v
	case []any:
		for i, child := range v {
			v[i] = redactValue(child)
		}
		return v
	default:
		return v
	}
}

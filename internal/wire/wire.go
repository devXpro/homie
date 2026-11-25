// Package wire holds the JSON conventions of the hub's own APIs, /api/home (internal/admin) and the first-run page's
// /api/firstrun (internal/firstrun): bodies, times, and MCP Gatekeeper's error form
//
//	{"error":"<code>","message":"…"}
//
// so the SPA reads the hub's answers and the gatekeeper's with one client. Some errors add flat keys next to the two:
// "fields" (a field that failed validation, with a JSON Pointer path), "callId", "jobId".
package wire

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MaxBody caps request bodies.
const MaxBody = 1 << 20

// Field is one field that failed validation; Path is a JSON Pointer into the request.
type Field struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is an API error with its HTTP status and stable code. Message is English and never holds a secret.
type Error struct {
	Status     int
	Code       string
	Message    string
	Fields     []Field
	Extra      map[string]string // flat keys next to "error" and "message", e.g. "callId"
	RetryAfter time.Duration     // sent as Retry-After (whole seconds, at least 1) when positive
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf returns an Error. The message is used as is.
func Errorf(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Invalid returns a 400 invalid_args error about one field.
func Invalid(path, code, message string) *Error {
	return &Error{
		Status: http.StatusBadRequest, Code: "invalid_args", Message: strings.TrimPrefix(path, "/") + ": " + message,
		Fields: []Field{{Path: path, Code: code, Message: message}},
	}
}

// Headers sets the headers every API answer carries: no caching and no sniffing.
func Headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// WriteJSON writes v with the status, without HTML escaping.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, err := Marshal(v)
	if err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// WriteError writes err in the error form. Anything but an *Error is answered as 500 internal without its text, so
// the caller logs it first.
func WriteError(w http.ResponseWriter, err error) {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		e = Errorf(http.StatusInternalServerError, "internal", "internal error")
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(e.RetryAfter.Seconds())))))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	_, _ = w.Write(ErrorBody(e))
}

// ErrorBody is the JSON of e: "error" and "message" first, then "fields", then the extra keys by name.
func ErrorBody(e *Error) []byte {
	var b bytes.Buffer
	b.WriteString("{")
	field := func(k string, v []byte) {
		if b.Len() > 1 {
			b.WriteString(",")
		}
		kj, _ := json.Marshal(k)
		b.Write(kj)
		b.WriteString(":")
		b.Write(v)
	}
	str := func(s string) []byte { j, _ := Marshal(s); return j }
	field("error", str(e.Code))
	field("message", str(e.Message))
	if len(e.Fields) > 0 {
		if j, err := Marshal(e.Fields); err == nil {
			field("fields", j)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(e.Extra)) {
		if k != "error" && k != "message" && k != "fields" {
			field(k, str(e.Extra[k]))
		}
	}
	b.WriteString("}")
	return b.Bytes()
}

// Decode reads a JSON object body into v. An empty body leaves v unchanged. It answers 413 too_large, 415
// unsupported_media_type, 400 invalid_json, or 400 invalid_args for a field of the wrong type.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return Errorf(http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
		}
		return Errorf(http.StatusBadRequest, "invalid_json", "the request body could not be read")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return Errorf(http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
	}
	if body[0] != '{' {
		return Errorf(http.StatusBadRequest, "invalid_json", "the body must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(v); err != nil {
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
			return Invalid("/"+strings.ReplaceAll(te.Field, ".", "/"), "type", "has the wrong type")
		}
		return Errorf(http.StatusBadRequest, "invalid_json", "the body is not valid JSON")
	}
	if dec.More() {
		return Errorf(http.StatusBadRequest, "invalid_json", "the body holds more than one JSON value")
	}
	return nil
}

// Marshal is json.Marshal without HTML escaping, so "<" in plugin texts arrives unchanged.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// Time formats t the way the APIs send times: RFC 3339, UTC, milliseconds ("2026-11-15T12:04:05.123Z").
func Time(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// OptTime is Time for optional times: "" (omitted with omitempty) when t is zero.
func OptTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return Time(t)
}

// Random returns n random bytes as base64url without padding.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails since Go 1.24; it aborts the program instead
	return base64.RawURLEncoding.EncodeToString(b)
}

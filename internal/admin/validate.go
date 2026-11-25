package admin

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/devXpro/homie/internal/hub"
	"github.com/devXpro/homie/internal/wire"
)

// Argument validation for admin tool calls with github.com/google/jsonschema-go (ADMIN-API §3.5). The library stops
// at the first error and reports it as text wrapped in "validating <schema path>: …"; this file turns that into
// the API's fields: a JSON Pointer into the arguments, a keyword code and a short English message.

type schemaCache struct {
	raw      string
	schema   *jsonschema.Schema
	resolved *jsonschema.Resolved
	err      error
}

// resolvedSchema returns the tool's resolved input schema, cached per tool until the schema changes.
func (s *Server) resolvedSchema(t hub.Tool) *schemaCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.schemas[t.Name]; ok && c.raw == string(t.InputSchema) {
		return c
	}
	c := &schemaCache{raw: string(t.InputSchema)}
	if len(t.InputSchema) > 0 {
		var sch jsonschema.Schema
		if c.err = json.Unmarshal(t.InputSchema, &sch); c.err == nil {
			c.schema = &sch
			c.resolved, c.err = sch.Resolve(nil)
		}
	}
	s.schemas[t.Name] = c
	return c
}

// validateArgs checks args against the tool's input schema and returns the failures; nil means valid. A schema the
// library cannot use lets the call through: the plugin validates again.
func (s *Server) validateArgs(t hub.Tool, args json.RawMessage) []wire.Field {
	var instance any
	if err := json.Unmarshal(args, &instance); err != nil {
		return []wire.Field{{Path: "/args", Code: "type", Message: "must be a JSON object"}}
	}
	if _, ok := instance.(map[string]any); !ok {
		return []wire.Field{{Path: "/args", Code: "type", Message: "must be a JSON object"}}
	}
	c := s.resolvedSchema(t)
	if c.resolved == nil {
		if c.err != nil {
			s.log.Warn("admin: tool input schema not usable for validation", "tool", t.Name, "err", c.err)
		}
		return nil
	}
	err := c.resolved.Validate(instance)
	if err == nil {
		return nil
	}
	return describe(c.schema, err.Error())
}

var (
	validatingRe = regexp.MustCompile(`validating ([^:]+): `)
	quotedRe     = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// describe turns the library's error text into fields.
func describe(root *jsonschema.Schema, msg string) []wire.Field {
	// The deepest "validating <path>" names the failing schema; the text after it is "<keyword>: <detail>".
	schemaPath := ""
	rest := msg
	if all := validatingRe.FindAllStringSubmatchIndex(msg, -1); len(all) > 0 {
		last := all[len(all)-1]
		schemaPath = msg[last[2]:last[3]]
		rest = msg[last[1]:]
	}
	if schemaPath == "root" {
		schemaPath = ""
	}
	instPath, sub := instancePath(root, schemaPath)
	keyword, detail, _ := strings.Cut(rest, ": ")
	keyword = strings.TrimSpace(keyword)

	switch {
	case keyword == "required":
		var out []wire.Field
		for _, m := range quotedRe.FindAllStringSubmatch(detail, -1) {
			out = append(out, wire.Field{Path: instPath + "/" + escapePointer(m[1]), Code: "required", Message: "is required"})
		}
		if len(out) > 0 {
			return out
		}
	case strings.HasPrefix(rest, "unexpected additional properties"):
		var out []wire.Field
		for _, m := range quotedRe.FindAllStringSubmatch(rest, -1) {
			out = append(out, wire.Field{Path: instPath + "/" + escapePointer(m[1]), Code: "additionalProperties", Message: "is not expected"})
		}
		if len(out) > 0 {
			return out
		}
	}
	return []wire.Field{{Path: pathOrArgs(instPath), Code: codeOf(keyword), Message: message(sub, keyword, rest)}}
}

func pathOrArgs(p string) string {
	if p == "" {
		return "/args"
	}
	return p
}

func codeOf(keyword string) string {
	if keyword == "" || strings.ContainsAny(keyword, " \t") {
		return "invalid"
	}
	return keyword
}

// instancePath maps a schema path such as "/properties/percent" to the argument pointer "/percent" and returns the
// subschema it points at (nil when it cannot be followed). Array items keep no index: the library does not say which.
func instancePath(root *jsonschema.Schema, schemaPath string) (string, *jsonschema.Schema) {
	sub := root
	var out strings.Builder
	segs := strings.Split(strings.TrimPrefix(schemaPath, "/"), "/")
	for i := 0; i < len(segs) && schemaPath != ""; i++ {
		seg := unescapePointer(segs[i])
		switch {
		case seg == "properties" && i+1 < len(segs):
			name := unescapePointer(segs[i+1])
			i++
			out.WriteString("/" + escapePointer(name))
			if sub != nil {
				sub = sub.Properties[name]
			}
		case seg == "items":
			if sub != nil {
				sub = sub.Items
			}
		default:
			sub = nil
		}
	}
	return out.String(), sub
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func unescapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

// message is a short English message for a keyword, using the schema's own limits.
func message(sub *jsonschema.Schema, keyword, fallback string) string {
	num := func(p *float64) string {
		if p == nil {
			return "?"
		}
		return strconv.FormatFloat(*p, 'f', -1, 64)
	}
	intp := func(p *int) string {
		if p == nil {
			return "?"
		}
		return strconv.Itoa(*p)
	}
	if sub == nil {
		return fallback
	}
	switch keyword {
	case "type":
		if sub.Type != "" {
			return "must be " + article(sub.Type)
		}
		return "must be one of the types " + strings.Join(sub.Types, ", ")
	case "enum":
		vals := make([]string, 0, len(sub.Enum))
		for _, v := range sub.Enum {
			b, _ := json.Marshal(v)
			vals = append(vals, string(b))
		}
		return "must be one of " + strings.Join(vals, ", ")
	case "const":
		return "has the wrong value"
	case "minimum":
		return "must be at least " + num(sub.Minimum)
	case "maximum":
		return "must be at most " + num(sub.Maximum)
	case "exclusiveMinimum":
		return "must be more than " + num(sub.ExclusiveMinimum)
	case "exclusiveMaximum":
		return "must be less than " + num(sub.ExclusiveMaximum)
	case "multipleOf":
		return "must be a multiple of " + num(sub.MultipleOf)
	case "minLength":
		return fmt.Sprintf("must be at least %s characters", intp(sub.MinLength))
	case "maxLength":
		return fmt.Sprintf("must be at most %s characters", intp(sub.MaxLength))
	case "pattern":
		return "has the wrong format"
	case "minItems":
		return fmt.Sprintf("must have at least %s items", intp(sub.MinItems))
	case "maxItems":
		return fmt.Sprintf("must have at most %s items", intp(sub.MaxItems))
	}
	return fallback
}

func article(typ string) string {
	switch typ {
	case "integer", "object", "array":
		return "an " + typ
	case "null":
		return "null"
	}
	return "a " + typ
}

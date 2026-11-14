// Package settings validates declarative inspector fields without losing extensions
package settings

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

type Field struct {
	PathPicker     string            `json:"path_picker,omitempty"`
	DefaultPresent bool              `json:"-"`
	Key            string            `json:"key"`
	Label          string            `json:"label"`
	Type           string            `json:"type"`
	Default        any               `json:"default,omitempty"`
	Options        []string          `json:"options,omitempty"`
	OptionLabels   map[string]string `json:"option_labels,omitempty"`
	Min            *float64          `json:"min,omitempty"`
	Max            *float64          `json:"max,omitempty"`
	Required       bool              `json:"required,omitempty"`
	// Sensitive masks string fields in inspectors; values remain ordinary strings
	Sensitive bool `json:"sensitive,omitempty"`
}

// UnmarshalJSON distinguishes an absent default from an explicit null default
func (f *Field) UnmarshalJSON(data []byte) error {
	type plain Field
	var parsed plain
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	*f = Field(parsed)
	_, f.DefaultPresent = keys["default"]
	return nil
}

// Clone supported settings values recursively
func Clone(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, x := range v {
			out[key] = Clone(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = Clone(x)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}

// Overlay action settings on cloned global defaults
func Merge(global, action map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range global {
		out[k] = Clone(v)
	}
	for k, v := range action {
		out[k] = Clone(v)
	}
	return out
}

// Validate applies defaults only to absent keys. Null is an explicit value and
// invalid for known fields; unknown keys are cloned and preserved
func Validate(fields []Field, input map[string]any) (map[string]any, error) {
	if len(fields) > 256 {
		return nil, fmt.Errorf("too many settings fields")
	}
	out := Merge(nil, input)
	seen := map[string]bool{}
	for _, f := range fields {
		if f.Key == "" || seen[f.Key] {
			return nil, fmt.Errorf("empty or duplicate settings field %q", f.Key)
		}
		seen[f.Key] = true
		if f.PathPicker != "" && (f.Type != "path" || (f.PathPicker != "file" && f.PathPicker != "folder" && f.PathPicker != "save")) {
			return nil, fmt.Errorf("%s: invalid path picker", f.Key)
		}
		if f.Sensitive && (f.Type == "number" || f.Type == "checkbox" || f.Type == "pairs") {
			return nil, fmt.Errorf("%s: sensitive requires a string field", f.Key)
		}
		if f.Min != nil && (math.IsNaN(*f.Min) || math.IsInf(*f.Min, 0)) || f.Max != nil && (math.IsNaN(*f.Max) || math.IsInf(*f.Max, 0)) || f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return nil, fmt.Errorf("%s: invalid bounds", f.Key)
		}
		switch f.Type {
		case "text", "textarea", "path", "device", "select", "checkbox", "number", "pairs":
		default:
			return nil, fmt.Errorf("%s: unknown field type %q", f.Key, f.Type)
		}
		value, exists := out[f.Key]
		if !exists {
			if f.Default != nil || f.DefaultPresent {
				value = Clone(f.Default)
				out[f.Key] = value
			} else if f.Required {
				return nil, fmt.Errorf("%s: required", f.Key)
			} else {
				continue
			}
		}
		valid := true
		switch f.Type {
		case "text", "textarea", "path", "device", "select":
			text, ok := value.(string)
			valid = ok && (!f.Required || text != "")
			if valid && f.Type == "select" {
				valid = false
				for _, option := range f.Options {
					if option == text {
						valid = true
					}
				}
			}
		case "checkbox":
			_, valid = value.(bool)
		case "number":
			var number float64
			switch n := value.(type) {
			case float64:
				number = n
			case int:
				number = float64(n)
			case int64:
				number = float64(n)
			case json.Number:
				var err error
				number, err = strconv.ParseFloat(string(n), 64)
				valid = err == nil
			default:
				valid = false
			}
			valid = valid && !math.IsNaN(number) && !math.IsInf(number, 0) && (f.Min == nil || number >= *f.Min) && (f.Max == nil || number <= *f.Max)
		case "pairs":
			pairs, ok := value.(map[string]any)
			valid = ok
			for _, v := range pairs {
				if _, ok := v.(string); !ok {
					valid = false
				}
			}
		default:
			return nil, fmt.Errorf("%s: unknown field type %q", f.Key, f.Type)
		}
		if !valid {
			return nil, fmt.Errorf("%s: invalid %s value", f.Key, f.Type)
		}
	}
	return out, nil
}

// Package mcpbridge adapts deployment-confirmed MCP bindings to typed delivery
// ports. Tool descriptions/model output are proposals, never write authority.
package mcpbridge

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

var ErrBinding = errors.New("MCP binding invalid or capability unavailable")
var ErrConnection = errors.New("MCP connection unavailable")

// Templates are JSON, not code. A reference is {"$path":"/input/key"}.
// Optional $map translates an exact enum; $as converts scalar representations.
// {"$each":"/result/items","template":...} binds each element as /item.
// Unknown paths/enums fail closed. There are no scripts, network or fallbacks.
func Render(template any, scope map[string]any) (any, error) {
	budget := 10000
	return render(template, scope, 0, &budget)
}
func render(t any, scope map[string]any, depth int, budget *int) (any, error) {
	*budget--
	if depth > 32 || *budget < 0 {
		return nil, ErrBinding
	}
	switch v := t.(type) {
	case map[string]any:
		if p, ok := v["$switch"]; ok {
			path, ok := p.(string)
			if !ok || len(v) != 2 {
				return nil, ErrBinding
			}
			x, err := pointer(scope, path)
			if err != nil {
				return nil, err
			}
			key, ok := x.(string)
			if !ok {
				return nil, ErrBinding
			}
			cases, ok := v["cases"].(map[string]any)
			if !ok {
				return nil, ErrBinding
			}
			value, ok := cases[key]
			if !ok {
				return nil, ErrBinding
			}
			return render(value, scope, depth+1, budget)
		}
		if p, ok := v["$each"]; ok {
			path, ok := p.(string)
			if !ok || len(v) != 2 {
				return nil, ErrBinding
			}
			a, err := pointer(scope, path)
			if err != nil {
				return nil, err
			}
			items, ok := a.([]any)
			if !ok || len(items) > 1000 {
				return nil, ErrBinding
			}
			out := make([]any, 0, len(items))
			for _, item := range items {
				s := map[string]any{}
				for k, x := range scope {
					s[k] = x
				}
				s["item"] = item
				x, err := render(v["template"], s, depth+1, budget)
				if err != nil {
					return nil, err
				}
				out = append(out, x)
			}
			return out, nil
		}
		if p, ok := v["$path"]; ok {
			path, ok := p.(string)
			if !ok {
				return nil, ErrBinding
			}
			for k := range v {
				if k != "$path" && k != "$map" && k != "$as" {
					return nil, ErrBinding
				}
			}
			x, err := pointer(scope, path)
			if err != nil {
				return nil, err
			}
			if format, ok := v["$as"]; ok {
				switch format {
				case "string":
					switch val := x.(type) {
					case string:
						x = val
					case json.Number:
						x = val.String()
					case float64:
						x = strconv.FormatFloat(val, 'f', -1, 64)
					case bool:
						x = strconv.FormatBool(val)
					default:
						return nil, ErrBinding
					}
				case "json":
					str, ok := x.(string)
					if !ok || len(str) > 1<<20 {
						return nil, ErrBinding
					}
					if err := decodeJSON([]byte(str), &x); err != nil {
						return nil, err
					}
				case "number":
					str, ok := x.(string)
					if !ok {
						return nil, ErrBinding
					}
					var number any
					if decodeJSON([]byte(str), &number) != nil {
						return nil, ErrBinding
					}
					if _, ok := number.(json.Number); !ok {
						return nil, ErrBinding
					}
					x = number
				case "boolean":
					switch x {
					case "true":
						x = true
					case "false":
						x = false
					default:
						return nil, ErrBinding
					}
				default:
					return nil, ErrBinding
				}
			}
			if m, ok := v["$map"]; ok {
				mapping, ok := m.(map[string]any)
				if !ok {
					return nil, ErrBinding
				}
				key, ok := x.(string)
				if !ok {
					return nil, ErrBinding
				}
				x, ok = mapping[key]
				if !ok {
					return nil, ErrBinding
				}
			}
			return x, nil
		}
		out := map[string]any{}
		for k, x := range v {
			if strings.HasPrefix(k, "$") {
				return nil, ErrBinding
			}
			value, err := render(x, scope, depth+1, budget)
			if err != nil {
				return nil, err
			}
			out[k] = value
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, x := range v {
			value, err := render(x, scope, depth+1, budget)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	case nil:
		return nil, ErrBinding
	default:
		return v, nil
	}
}

func pointer(root any, path string) (any, error) {
	if path == "" {
		return root, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, ErrBinding
	}
	for _, part := range strings.Split(path[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch value := root.(type) {
		case map[string]any:
			var ok bool
			root, ok = value[part]
			if !ok {
				return nil, ErrBinding
			}
		case []any:
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n >= len(value) {
				return nil, ErrBinding
			}
			root = value[n]
		default:
			return nil, ErrBinding
		}
	}
	if root == nil {
		return nil, ErrBinding
	}
	return root, nil
}

func decodeJSON(data []byte, dst any) error {
	// Reject duplicate keys at every depth, including opaque templates/schemas.
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := uniqueJSON(dec, 0); err != nil {
		return ErrBinding
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrBinding
	}
	dec = json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return ErrBinding
	}
	return nil
}
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrBinding
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); ok {
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return ErrBinding
				}
				seen[key] = true
			}
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

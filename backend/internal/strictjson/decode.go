// Package strictjson rejects duplicate, unknown, case-aliased and null fields
// for deployment configuration and new typed change commands.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

var ErrInvalid = errors.New("invalid JSON fields")

func Decode(data []byte, destination any) error {
	t := reflect.TypeOf(destination)
	if t == nil || t.Kind() != reflect.Pointer {
		return ErrInvalid
	}
	if err := check(data, t.Elem()); err != nil {
		return ErrInvalid
	}
	return json.Unmarshal(data, destination)
}
func fields(t reflect.Type) map[string]reflect.Type {
	result := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for k, v := range fields(f.Type) {
				result[k] = v
			}
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			result[name] = f.Type
		}
	}
	return result
}
func check(data []byte, t reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrInvalid
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		decoder := json.NewDecoder(bytes.NewReader(data))
		opening, err := decoder.Token()
		if err != nil || opening != json.Delim('{') {
			return ErrInvalid
		}
		allowed, seen := fields(t), map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			field, known := allowed[key]
			if err != nil || !ok || !known || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil || check(raw, field) != nil {
				return ErrInvalid
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
			return ErrInvalid
		}
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return ErrInvalid
		}
		for _, item := range items {
			if check(item, t.Elem()) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

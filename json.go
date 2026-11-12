package balance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

const maxJSONBytes = 8 << 20

// decodeStrict checks exact field names and duplicate keys before decoding.
// The standard decoder otherwise accepts case aliases and repeated fields.
func decodeStrict(reader io.Reader, output any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxJSONBytes+1))
	if err != nil {
		return fmt.Errorf("read JSON: %w", err)
	}
	if len(data) > maxJSONBytes {
		return fmt.Errorf("JSON exceeds the 8 MiB input limit")
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON contains invalid UTF-8")
	}
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	if err := scanJSON(scanner, reflect.TypeOf(output).Elem(), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}

func scanJSON(decoder *json.Decoder, kind reflect.Type, depth int) error {
	if depth > 32 {
		return fmt.Errorf("JSON nesting exceeds 32 levels")
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		var child reflect.Type
		if token == json.Delim('{') {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("JSON object key must be a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON key %q", name)
			}
			seen[name] = true
			child = jsonFieldType(kind, name)
			if child == nil {
				return fmt.Errorf("unknown JSON field %q", name)
			}
		} else {
			if kind.Kind() != reflect.Slice && kind.Kind() != reflect.Array {
				return fmt.Errorf("unexpected JSON array")
			}
			child = kind.Elem()
		}
		if err := scanJSON(decoder, child, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func jsonFieldType(kind reflect.Type, name string) reflect.Type {
	if kind.Kind() == reflect.Map {
		return kind.Elem()
	}
	if kind.Kind() == reflect.Struct {
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			if field.IsExported() && strings.Split(field.Tag.Get("json"), ",")[0] == name {
				return field.Type
			}
		}
	}
	return nil
}

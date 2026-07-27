package perfdata

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Ordered-JSON helpers for tools that repair a field in place.
//
// encoding/json unmarshals objects into maps, which lose key order, and
// round-trips numbers through float64, which can change their text. A repair tool
// must leave everything it isn't fixing exactly as it found it — otherwise the
// diff is unreviewable and "we only changed X" is unverifiable. These keep values
// as verbatim raw bytes and recurse only where a change is actually needed.

// OrderedObject is a JSON object that remembers key order and holds each value as
// the exact bytes it was parsed from.
type OrderedObject struct {
	Keys []string
	Vals map[string]json.RawMessage
}

// Get returns the raw value for a key.
func (o *OrderedObject) Get(k string) (json.RawMessage, bool) {
	v, ok := o.Vals[k]
	return v, ok
}

// Set replaces a key's value, keeping its position. Setting an absent key appends.
func (o *OrderedObject) Set(k string, v json.RawMessage) {
	if _, exists := o.Vals[k]; !exists {
		o.Keys = append(o.Keys, k)
	}
	o.Vals[k] = v
}

// ParseObject decodes a JSON object preserving key order and verbatim values.
func ParseObject(raw []byte) (*OrderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object, got %v", tok)
	}
	o := &OrderedObject{Vals: map[string]json.RawMessage{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key, got %v", kt)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.Keys = append(o.Keys, k)
		o.Vals[k] = v
	}
	return o, nil
}

// Encode re-emits the object compactly, in original key order, with untouched
// values byte-for-byte as parsed.
func (o *OrderedObject) Encode() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.Vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// ParseArray decodes a JSON array into verbatim element bytes.
func ParseArray(raw []byte) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// EncodeArray re-emits array elements compactly, verbatim.
func EncodeArray(items []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return b.Bytes()
}

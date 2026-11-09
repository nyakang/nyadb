// Package graphimport defines portable graph exchange without drivers or UI.
package graphimport

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const Format = "nyadb.graph.v1"
const MaxBytes = 8 << 20
const MaxEntities = 10000
const MaxEntityBytes = 1 << 20

type header struct {
	Format string
	Source contract.NamespaceRef
}

func Parse(r io.Reader, format string) (contract.GraphImportBundle, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return contract.GraphImportBundle{}, err
	}
	if len(raw) > MaxBytes {
		return contract.GraphImportBundle{}, jsondocument.ErrBounds
	}
	var b contract.GraphImportBundle
	switch strings.ToLower(format) {
	case "json":
		if err = decode(raw, &b, MaxBytes); err != nil {
			return b, err
		}
	case "jsonl":
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), MaxEntityBytes+1)
		first := true
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			if first {
				var h header
				if err = decode(line, &h, MaxEntityBytes); err != nil {
					return b, err
				}
				b.Format, b.Source, first = h.Format, h.Source, false
				continue
			}
			var v contract.GraphValue
			if err = decode(line, &v, MaxEntityBytes); err != nil {
				return b, err
			}
			if v.Text != "" || len(v.Values) != 0 || len(v.Properties) != 0 || v.Path != nil {
				return b, jsondocument.ErrInvalid
			}
			switch {
			case v.Kind == "node" && v.Node != nil && v.Relationship == nil:
				b.Nodes = append(b.Nodes, *v.Node)
			case v.Kind == "relationship" && v.Relationship != nil && v.Node == nil:
				b.Relationships = append(b.Relationships, *v.Relationship)
			default:
				return b, jsondocument.ErrInvalid
			}
			if len(b.Nodes)+len(b.Relationships) > MaxEntities {
				return b, jsondocument.ErrBounds
			}
		}
		if scanner.Err() != nil {
			return b, jsondocument.ErrBounds
		}
	default:
		return b, contract.ErrUnsupported
	}
	return b, Validate(b)
}

func decode(raw []byte, target any, maxBytes int) error {
	if err := jsondocument.Decode(raw, target, maxBytes); err != nil {
		return err
	}
	if !exactFields(raw, reflect.TypeOf(target).Elem()) {
		return jsondocument.ErrInvalid
	}
	return nil
}

// JSON struct decoding folds case. Check the exact schema recursively before
// accepting typed entity IDs, labels, property kinds or hidden components.
func exactFields(raw []byte, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
		return exactFields(raw, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return false
		}
		for name, value := range fields {
			f, ok := typ.FieldByName(name)
			if !ok || f.PkgPath != "" || !exactFields(value, f.Type) {
				return false
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !exactFields(value, typ.Elem()) {
				return false
			}
		}
	default:
		// encoding/json treats null as a no-op for primitive fields. Reject it
		// so an explicit null cannot silently become an empty string or zero.
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	return true
}

func name(s string, maxBytes int) bool {
	return s != "" && utf8.ValidString(s) && len(s) <= maxBytes && !strings.ContainsRune(s, 0)
}

func Validate(b contract.GraphImportBundle) error {
	if b.Format != Format {
		return fmt.Errorf("%w: graph exchange format", contract.ErrUnsupported)
	}
	if len(b.Nodes)+len(b.Relationships) > MaxEntities {
		return jsondocument.ErrBounds
	}
	nodes, relationships := map[string]bool{}, map[string]bool{}
	for _, node := range b.Nodes {
		if !name(node.ElementID, 1024) || nodes[node.ElementID] {
			return fmt.Errorf("%w: duplicate or invalid source node ID", jsondocument.ErrInvalid)
		}
		nodes[node.ElementID] = true
		labels := map[string]bool{}
		for _, label := range node.Labels {
			if !name(label, 65535) || labels[label] {
				return fmt.Errorf("%w: node labels", jsondocument.ErrInvalid)
			}
			labels[label] = true
		}
		if err := properties(node.Properties); err != nil {
			return err
		}
		if raw, err := json.Marshal(contract.GraphValue{Kind: "node", Node: &node}); err != nil || len(raw) > MaxEntityBytes {
			return jsondocument.ErrBounds
		}
	}
	for _, relationship := range b.Relationships {
		if !name(relationship.ElementID, 1024) || relationships[relationship.ElementID] || !name(relationship.StartElementID, 1024) || !name(relationship.EndElementID, 1024) || !name(relationship.Type, 65535) {
			return fmt.Errorf("%w: relationship identity or type", jsondocument.ErrInvalid)
		}
		relationships[relationship.ElementID] = true
		if err := properties(relationship.Properties); err != nil {
			return err
		}
		if raw, err := json.Marshal(contract.GraphValue{Kind: "relationship", Relationship: &relationship}); err != nil || len(raw) > MaxEntityBytes {
			return jsondocument.ErrBounds
		}
	}
	if raw, err := json.Marshal(b); err != nil || len(raw) > MaxBytes {
		return jsondocument.ErrBounds
	}
	return nil
}

func properties(props []contract.GraphProperty) error {
	seen := map[string]bool{}
	for _, prop := range props {
		if !name(prop.Name, 65535) || seen[prop.Name] {
			return fmt.Errorf("%w: property names", jsondocument.ErrInvalid)
		}
		seen[prop.Name] = true
		v := prop.Value
		if v.Kind == "null" {
			return fmt.Errorf("%w: null is absence, not a graph property", contract.ErrUnsupported)
		}
		if v.Kind == "list" {
			if _, err := contract.GraphPropertyListElementKind(v); err != nil {
				return err
			}
		} else if v.Kind == "bytes" {
			if len(v.Values) != 0 || len(v.Properties) != 0 || v.Node != nil || v.Relationship != nil || v.Path != nil {
				return contract.ErrUnsupported
			}
			raw, err := base64.StdEncoding.Strict().DecodeString(v.Text)
			if err != nil || base64.StdEncoding.EncodeToString(raw) != v.Text {
				return jsondocument.ErrInvalid
			}
		} else if _, err := contract.GraphPropertyScalarValue(v); err != nil {
			if _, err = contract.GraphPropertyExtendedValue(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Endpoint references absent from the file must be mapped explicitly. Source
// IDs are never looked up as destination element IDs automatically.
func ExternalEndpoints(b contract.GraphImportBundle) []string {
	nodes, missing := map[string]bool{}, map[string]bool{}
	for _, n := range b.Nodes {
		nodes[n.ElementID] = true
	}
	for _, r := range b.Relationships {
		for _, id := range []string{r.StartElementID, r.EndElementID} {
			if !nodes[id] {
				missing[id] = true
			}
		}
	}
	ids := make([]string, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func ValidateRequest(r contract.GraphImportRequest) error {
	if err := Validate(r.Bundle); err != nil {
		return err
	}
	available := map[string]bool{}
	for _, n := range r.Bundle.Nodes {
		available[n.ElementID] = true
	}
	for _, id := range ExternalEndpoints(r.Bundle) {
		available[id] = true
	}
	bindings := map[string]bool{}
	for _, binding := range r.Bindings {
		if !available[binding.SourceID] || bindings[binding.SourceID] || !name(binding.DestinationID, 1024) {
			return fmt.Errorf("%w: explicit endpoint mapping", jsondocument.ErrInvalid)
		}
		bindings[binding.SourceID] = true
	}
	creates := len(r.Bundle.Relationships)
	for _, node := range r.Bundle.Nodes {
		if !bindings[node.ElementID] {
			creates++
		}
	}
	if creates == 0 {
		return fmt.Errorf("%w: empty graph import", jsondocument.ErrInvalid)
	}
	for _, id := range ExternalEndpoints(r.Bundle) {
		if !bindings[id] {
			return fmt.Errorf("%w: unmapped endpoint %q", jsondocument.ErrInvalid, id)
		}
	}
	if raw, err := json.Marshal(r); err != nil || len(raw) > MaxBytes {
		return jsondocument.ErrBounds
	}
	return nil
}

func Write(w io.Writer, b contract.GraphImportBundle, format string) error {
	if err := Validate(b); err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	switch strings.ToLower(format) {
	case "json":
		if err := encoder.Encode(b); err != nil {
			return err
		}
	case "jsonl":
		if err := encoder.Encode(header{Format: b.Format, Source: b.Source}); err != nil {
			return err
		}
		for _, n := range b.Nodes {
			if err := encoder.Encode(contract.GraphValue{Kind: "node", Node: &n}); err != nil {
				return err
			}
		}
		for _, r := range b.Relationships {
			if err := encoder.Encode(contract.GraphValue{Kind: "relationship", Relationship: &r}); err != nil {
				return err
			}
		}
	default:
		return contract.ErrUnsupported
	}
	if buffer.Len() > MaxBytes {
		return jsondocument.ErrBounds
	}
	n, err := w.Write(buffer.Bytes())
	if err == nil && n != buffer.Len() {
		return io.ErrShortWrite
	}
	return err
}

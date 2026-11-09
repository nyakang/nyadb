package graphimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func exchange(t *testing.T) contract.GraphImportBundle {
	t.Helper()
	props := []contract.GraphProperty{}
	for _, v := range []contract.GraphValue{{Kind: "string", Text: "图\n\""}, {Kind: "integer", Text: "9223372036854775807"}, {Kind: "float", Text: "-0"}, {Kind: "boolean", Text: "true"}, {Kind: "bytes", Text: "AP8="}, {Kind: "list"}} {
		props = append(props, contract.GraphProperty{Name: v.Kind, Value: v})
	}
	for _, sample := range []struct{ kind, text string }{
		{"date", "10000-02-29"}, {"local_time", "23:59:59.999999999"}, {"local_datetime", "10000-02-29T00:00:00"},
		{"time", `{"value":"01:02:03+00:20","offset_seconds":1234}`},
		{"datetime", `{"value":"2026-01-01T00:00:00Z","offset_seconds":0,"zone":"UTC"}`},
		{"duration", `{"months":-1,"days":2,"seconds":3,"nanoseconds":4}`},
		{"point2d", `{"srid":7203,"x":1,"y":-0}`}, {"point3d", `{"srid":4979,"x":1,"y":2,"z":3}`},
	} {
		v, err := contract.ParseGraphPropertyExtended(sample.kind, sample.text)
		if err != nil {
			t.Fatal(err)
		}
		props = append(props, contract.GraphProperty{Name: sample.kind, Value: v}, contract.GraphProperty{Name: sample.kind + "_list", Value: contract.GraphValue{Kind: "list", Values: []contract.GraphValue{v.Clone(), v.Clone()}}})
	}
	return contract.GraphImportBundle{Format: Format, Source: contract.NamespaceRef{ProfileID: 7, Catalog: "source"}, Nodes: []contract.GraphNode{
		{ElementID: "source-a", LegacyID: 9223372036854775807, Labels: []string{"A", "中文 label"}, Properties: props},
		{ElementID: "source-b"},
	}, Relationships: []contract.GraphRelationship{
		{ElementID: "source-a", Type: "self", StartElementID: "source-a", EndElementID: "source-a", Properties: props},
		{ElementID: "edge", Type: "reverse", StartElementID: "source-b", EndElementID: "source-a"},
	}}
}

func TestExchangeRoundtripRetainsAllTypedPropertiesAndSourceReferences(t *testing.T) {
	b := exchange(t)
	for _, format := range []string{"json", "jsonl"} {
		var out bytes.Buffer
		if err := Write(&out, b, format); err != nil {
			t.Fatal(err)
		}
		got, err := Parse(&out, format)
		if err != nil || !reflect.DeepEqual(got, b) {
			t.Fatal(format, "complete typed exchange changed", err)
		}
	}
}

func TestExchangeRejectsAmbiguousSchemaAndMalformedValues(t *testing.T) {
	raw, _ := json.Marshal(exchange(t))
	for _, bad := range []string{
		strings.Replace(string(raw), `"Format":`, `"format":`, 1),
		strings.Replace(string(raw), `"ElementID":`, `"elementid":`, 1),
		strings.Replace(string(raw), `"Kind":"string"`, `"Kind":"string","kind":"integer"`, 1),
		strings.Replace(string(raw), `"Kind":"string"`, `"Kind":"string","Kind":"integer"`, 1),
		strings.Replace(string(raw), `"Kind":"bytes","Text":"AP8="`, `"Kind":"bytes","Text":"AP9="`, 1),
		strings.Replace(string(raw), `"Kind":"boolean","Text":"true"`, `"Kind":"boolean","Text":"yes"`, 1),
		strings.Replace(string(raw), `"Kind":"integer","Text":"9223372036854775807"`, `"Kind":"integer","Text":"9223372036854775808"`, 1),
		`{"Format":"nyadb.graph.v1","Nodes":[{"ElementID":"\ud800"}]}`,
		`{"Format":"nyadb.graph.v1","extra":0}`,
		`{"Format":"nyadb.graph.v1","Nodes":[{"ElementID":"a","Properties":[{"Name":"p","Value":{"Kind":"null"}}]}]}`,
		`{"Format":"nyadb.graph.v1","Nodes":[{"ElementID":"a","Properties":[{"Name":"p","Value":{"Kind":"string","Text":null}}]}]}`,
		string(raw) + `{}`,
	} {
		if _, err := Parse(strings.NewReader(bad), "json"); err == nil {
			t.Fatal("ambiguous exchange accepted", bad[:min(len(bad), 100)])
		}
	}
	for _, change := range []func(*contract.GraphImportBundle){
		func(b *contract.GraphImportBundle) { b.Nodes = append(b.Nodes, b.Nodes[0]) },
		func(b *contract.GraphImportBundle) { b.Relationships = append(b.Relationships, b.Relationships[0]) },
		func(b *contract.GraphImportBundle) {
			b.Nodes[0].Labels = append(b.Nodes[0].Labels, b.Nodes[0].Labels[0])
		},
		func(b *contract.GraphImportBundle) {
			b.Nodes[0].Properties = append(b.Nodes[0].Properties, b.Nodes[0].Properties[0])
		},
		func(b *contract.GraphImportBundle) {
			b.Nodes[0].Properties[0].Value = contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "string", Text: "x"}, {Kind: "integer", Text: "1"}}}
		},
		func(b *contract.GraphImportBundle) {
			b.Nodes[0].Properties[0].Value = contract.GraphValue{Kind: "list", Values: []contract.GraphValue{{Kind: "list"}}}
		},
	} {
		b := exchange(t)
		change(&b)
		if err := Validate(b); err == nil {
			t.Fatal("invalid graph admitted")
		}
	}
}

func TestExchangeRequiresExplicitExternalEndpointsAndAllowsIncludedNodeReuse(t *testing.T) {
	b := exchange(t)
	b.Relationships[0].EndElementID = "outside-z"
	b.Relationships[1].StartElementID = "outside-a"
	if got := ExternalEndpoints(b); !reflect.DeepEqual(got, []string{"outside-a", "outside-z"}) {
		t.Fatal(got)
	}
	r := contract.GraphImportRequest{Bundle: b}
	if ValidateRequest(r) == nil {
		t.Fatal("source IDs implicitly authorized destination nodes")
	}
	r.Bindings = []contract.GraphImportBinding{{SourceID: "outside-z", DestinationID: "destination"}, {SourceID: "outside-a", DestinationID: "destination"}, {SourceID: "source-a", DestinationID: "reused"}}
	if err := ValidateRequest(r); err != nil {
		t.Fatal(err)
	}
	r.Bindings = append(r.Bindings, r.Bindings[0])
	if ValidateRequest(r) == nil {
		t.Fatal("duplicate binding accepted")
	}
	r.Bindings = r.Bindings[:3]
	r.Bindings[0].SourceID = "unknown"
	if ValidateRequest(r) == nil {
		t.Fatal("unknown binding accepted")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestExchangeEmptyGraphNoopAndBudgets(t *testing.T) {
	empty := contract.GraphImportBundle{Format: Format}
	var out bytes.Buffer
	if err := Write(&out, empty, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(&out, "jsonl"); err != nil {
		t.Fatal(err)
	}
	if ValidateRequest(contract.GraphImportRequest{Bundle: empty}) == nil {
		t.Fatal("no-op import accepted")
	}
	b := contract.GraphImportBundle{Format: Format, Nodes: []contract.GraphNode{{ElementID: "a"}}}
	if ValidateRequest(contract.GraphImportRequest{Bundle: b, Bindings: []contract.GraphImportBinding{{SourceID: "a", DestinationID: "x"}}}) == nil {
		t.Fatal("reuse-only no-op accepted")
	}
	if err := Write(shortWriter{}, b, "json"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write reported success", err)
	}
	b.Nodes[0].Properties = []contract.GraphProperty{{Name: "large", Value: contract.GraphValue{Kind: "string", Text: strings.Repeat("x", MaxEntityBytes)}}}
	if Validate(b) == nil {
		t.Fatal("complete entity budget ignored")
	}
	b.Nodes = make([]contract.GraphNode, MaxEntities+1)
	for i := range b.Nodes {
		b.Nodes[i].ElementID = fmt.Sprint(i)
	}
	if Validate(b) == nil {
		t.Fatal("entity count budget ignored")
	}
	if _, err := Parse(strings.NewReader(strings.Repeat(" ", MaxBytes+1)), "json"); err == nil {
		t.Fatal("input source budget ignored")
	}
	if _, err := Parse(strings.NewReader(`{"Format":"nyadb.graph.v1"}`), "csv"); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestExchangeClonesDeepTypedComponentsAndBindings(t *testing.T) {
	r := contract.GraphImportRequest{Bundle: exchange(t), Bindings: []contract.GraphImportBinding{{SourceID: "source-b", DestinationID: "target"}}}
	s := contract.GraphImportSnapshot{Request: r, Anchors: []contract.GraphImportAnchor{{SourceID: "source-b", Node: r.Bundle.Nodes[0]}}}
	c := contract.CloneGraphImportSnapshot(s)
	c.Request.Bundle.Nodes[0].Properties[0].Value.Text = "changed"
	c.Request.Bundle.Relationships[0].Properties[0].Value.Text = "changed"
	c.Request.Bindings[0].DestinationID = "changed"
	c.Anchors[0].Node.Labels[0] = "changed"
	if s.Request.Bundle.Nodes[0].Properties[0].Value.Text == "changed" || s.Request.Bundle.Relationships[0].Properties[0].Value.Text == "changed" || s.Request.Bindings[0].DestinationID == "changed" || s.Anchors[0].Node.Labels[0] == "changed" {
		t.Fatal("clone retained shared mutable source")
	}
}

func TestExchangeCanonicalComparisonPreservesReferencesKindsAndExactComponents(t *testing.T) {
	r := contract.GraphImportRequest{Bundle: exchange(t)}
	copy := contract.CloneGraphImportRequest(r)
	copy.Bundle.Nodes[0].Labels[0], copy.Bundle.Nodes[0].Labels[1] = copy.Bundle.Nodes[0].Labels[1], copy.Bundle.Nodes[0].Labels[0]
	copy.Bundle.Nodes[0].Properties[2].Value.Text = "-0.0"
	if !EquivalentRequest(r, copy) {
		t.Fatal("native canonical spelling or label order changed graph semantics")
	}
	copy.Bundle.Nodes[0].Properties[2].Value.Text = "0"
	if EquivalentRequest(r, copy) {
		t.Fatal("canonical comparison lost negative zero")
	}
	copy = contract.CloneGraphImportRequest(r)
	copy.Bundle.Relationships[0].EndElementID = "source-b"
	if EquivalentRequest(r, copy) {
		t.Fatal("canonical comparison changed a relationship endpoint")
	}
	copy = contract.CloneGraphImportRequest(r)
	copy.Bundle.Nodes[0].Properties[0].Value.Kind = "bytes"
	if EquivalentRequest(r, copy) {
		t.Fatal("canonical comparison changed property kind")
	}
}

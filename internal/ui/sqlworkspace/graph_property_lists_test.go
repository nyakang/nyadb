package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func graphListValueTest(t *testing.T, kind, text string) contract.GraphValue {
	t.Helper()
	value, err := contract.ParseGraphPropertyList(kind, text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestGraphPropertyListPreviewDeepCaptureParkSnapshotAndOriginalRecords(t *testing.T) {
	requests := make(chan contract.GraphPropertyWriteRequest, 1)
	release := make(chan struct{})
	s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		requests <- r.Clone()
		<-release
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	g := d.graphVisualState()
	old := graphListValueTest(t, "integer", "[9223372036854775807]")
	g.applyGraphPropertyEntity(contract.GraphPropertyTarget{Kind: "node", ElementID: "a"}, contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: "a", Properties: []contract.GraphProperty{{Name: "values", Value: old}}}})
	g.focus("n:a")
	original, _ := json.Marshal(d.result)
	s.openGraphPropertyDraft(d, g, g.properties()[0])
	p := d.graphPropertyDraft
	if p == nil || p.kind != "list:integer" || p.text != "[9223372036854775807]" {
		t.Fatal("list draft flattened values or lost the exact old integer")
	}
	p.text = "[-9223372036854775808,9007199254740993]"
	s.previewGraphProperty(d)
	if p.preview == nil || p.preview.After.Kind != "list" || p.preview.After.Text != "" || p.preview.After.Values[0].Kind != "integer" {
		t.Fatal("list preview did not retain typed elements", p.error)
	}
	active := true
	s.deps.Active = func(*Document) bool { return active }
	s.saveGraphProperty(d)
	r := <-requests
	p.preview.Before.Values[0].Text = "1"
	p.preview.After.Values[0].Text = "2"
	p.request.Before.Values[0].Text = "3"
	p.text = "[4]"
	active = false
	close(release)
	s.Consume()
	if !d.Loading() || len(d.graphPropertyRecords) != 0 {
		t.Fatal("parked list completion published on another profile")
	}
	active = true
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if d.graphPropertyDraft != nil || l.writes.Load() != 1 || len(d.graphPropertyRecords) != 1 || !reflect.DeepEqual(d.graphPropertyRecords[0].Request, r) || g.properties()[0].Value.Values[0].Text != "-9223372036854775808" || g.properties()[0].Value.Values[1].Text != "9007199254740993" {
		t.Fatal("list worker/receipt shared mutable preview storage or lost integer precision")
	}
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snapshot := d.snapshots[0].Document
	d.graphPropertyRecords[0].Receipt.Result.Rows[0][2].Values[0].Text = "changed after snapshot"
	if snapshot.graphPropertyRecords[0].Receipt.Result.Rows[0][2].Values[0].Text != "-9223372036854775808" || snapshot.graphVisualState().properties()[0].Value.Values[0].Text != "-9223372036854775808" {
		t.Fatal("list snapshot shared receipt or entity element storage")
	}
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(original, after) {
		t.Fatal("list editing changed the original query records")
	}
}

func TestGraphPropertyListLocalizedSelectionEmptyEditingAndUnsupportedKinds(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })
	s, d, _ := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	g := d.graphVisualState()
	g.focus("n:a")
	s.openGraphPropertyCreation(d, g)
	p := d.graphPropertyDraft
	p.name = "typed list"
	s.previewGraphProperty(d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 720, 520)
	tt.Frame()
	if err := tt.Click(locales.Text("图属性类型")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Integer list"); err != nil {
		t.Fatal(err)
	}
	if p.kind != "list:integer" || p.text != "[]" || p.preview != nil {
		t.Fatal("list kind selection kept a scalar value or stale preview")
	}
	p.text = "[-9223372036854775808]"
	s.previewGraphProperty(d)
	if p.preview == nil || p.preview.After.Values[0].Text != "-9223372036854775808" {
		t.Fatal("integer list preview lost native range", p.error)
	}
	tt.Frame()
	if err := tt.Click(locales.Text("图属性类型")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Boolean list"); err != nil {
		t.Fatal(err)
	}
	if p.preview != nil || p.text != "[-9223372036854775808]" {
		t.Fatal("list type change retained a preview or silently erased the draft")
	}
	s.previewGraphProperty(d)
	if p.preview != nil || p.error == "" {
		t.Fatal("integer elements were silently converted to booleans")
	}
	p.text = "[false]"
	s.previewGraphProperty(d)
	if p.preview == nil || p.preview.After.Values[0].Kind != "boolean" || p.preview.After.Values[0].Text != "false" {
		t.Fatal("boolean list selection lost false")
	}
	d.graphPropertyDraft = nil
	d.graphPropertyOpen = false
	g.applyGraphPropertyEntity(contract.GraphPropertyTarget{Kind: "node", ElementID: "a"}, contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: "a", Properties: []contract.GraphProperty{{Name: "empty", Value: contract.GraphValue{Kind: "list"}}}}})
	s.openGraphPropertyDraft(d, g, g.properties()[0])
	p = d.graphPropertyDraft
	s.previewGraphProperty(d)
	if p.preview != nil || p.error != "属性值未更改" {
		t.Fatal("empty list nil/empty storage shape invented a changed value")
	}
	p.kind, p.text = "list:float", "[1]"
	s.previewGraphProperty(d)
	if p.preview == nil || p.preview.Before.Kind != "list" || len(p.preview.Before.Values) != 0 || p.preview.After.Values[0].Kind != "float" {
		t.Fatal("editing an empty list did not use the explicitly selected kind")
	}
	d.graphPropertyDraft = nil
	for _, value := range []contract.GraphValue{{Kind: "list", Values: []contract.GraphValue{{Kind: "null"}}}, {Kind: "list", Values: []contract.GraphValue{{Kind: "integer", Text: "1"}, {Kind: "float", Text: "1"}}}, {Kind: "list", Values: []contract.GraphValue{{Kind: "date", Text: "2026-11-09"}}}, {Kind: "list", Values: []contract.GraphValue{{Kind: "list"}}}} {
		if s.canEditGraphProperty(d, g, contract.GraphProperty{Name: "unsupported", Value: value}) {
			t.Fatal("unsupported or untyped list property was advertised as editable")
		}
	}
}

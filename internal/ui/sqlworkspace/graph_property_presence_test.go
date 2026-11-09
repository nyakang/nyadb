package sqlworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestGraphPropertyPresencePreviewUIChangesParkedCreationAndIndependentRemoval(t *testing.T) {
	started := make(chan contract.GraphPropertyWriteRequest, 1)
	release := make(chan struct{})
	s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		started <- r.Clone()
		<-release
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	active := true
	s.deps.Active = func(*Document) bool { return active }
	original, _ := json.Marshal(d.result)
	g := d.graphVisualState()
	g.focus("n:a")
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click(locales.Text("新增图属性")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(locales.Text("图属性名称")); err != nil {
		t.Fatal(err)
	}
	tt.Type("new key")
	if err := tt.Click(locales.Text("预览属性保存")); err != nil {
		t.Fatal(err)
	}
	p := d.graphPropertyDraft
	if p.preview == nil || p.preview.Property != "new key" || p.preview.Before.Kind != "null" || p.preview.After.Kind != "string" || p.preview.After.Text != "" {
		t.Fatal("UI creation confused an absent property with a present empty string", p.error)
	}
	if err := tt.Click(locales.Text("图属性名称")); err != nil {
		t.Fatal(err)
	}
	tt.Key(commandModifier(), ui.KeyA)
	tt.Type("new 猫 ` key")
	if p.preview != nil {
		t.Fatal("editing a creation name retained its old immutable preview")
	}
	if err := tt.Click(locales.Text("预览属性保存")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(locales.Text("确认新增图属性")); err != nil {
		t.Fatal(err)
	}
	r := <-started
	if r.Property != "new 猫 ` key" || r.Namespace != d.Namespace() || r.Target.ElementID != "a" || r.Before.Kind != "null" || r.After.Kind != "string" {
		t.Fatal("creation did not capture the original qualified request", r)
	}
	p.name, p.kind, p.text = "later name", "integer", "123"
	active = false
	close(release)
	s.Consume()
	if !d.Loading() || len(d.graphPropertyRecords) != 0 {
		t.Fatal("parked creation was published on another profile")
	}
	active = true
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if d.graphPropertyDraft != nil || len(d.graphPropertyRecords) != 1 || d.graphPropertyRecords[0].Request.Property != r.Property || g.properties()[0].Name != r.Property || g.properties()[0].Value.Kind != "string" || g.properties()[0].Value.Text != "" || l.writes.Load() != 1 {
		t.Fatal("empty-string creation was lost, mutated or replayed")
	}
	if !s.pinResult(d) {
		t.Fatal(d.Error())
	}
	snapshot := d.snapshots[0].Document
	if s.canTargetGraphProperty(snapshot, snapshot.graphVisualState()) {
		t.Fatal("snapshot allowed a live property creation")
	}
	d.graphPropertyOpen = false
	tt.Frame()
	if err := tt.Click(r.Property + " " + locales.Text("移除图属性")); err != nil {
		t.Fatal(err)
	}
	if d.graphPropertyDraft == nil || !d.graphPropertyDraft.remove || d.graphPropertyDraft.preview != nil {
		t.Fatal("removal did not require a new explicit preview")
	}
	if err := tt.Click(locales.Text("预览属性保存")); err != nil {
		t.Fatal(err)
	}
	if d.graphPropertyDraft.preview.After.Kind != "null" || d.graphPropertyDraft.preview.Before.Kind != "string" {
		t.Fatal("removal preview did not retain the present typed old value")
	}
	if err := tt.Click(locales.Text("确认移除图属性")); err != nil {
		t.Fatal(err)
	}
	<-started
	consumeUntil(t, s, func() bool { return !d.Loading() })
	if len(g.properties()) != 0 || !s.canTargetGraphProperty(d, g) || len(snapshot.graphVisualState().properties()) != 1 || len(snapshot.graphPropertyRecords) != 1 || l.writes.Load() != 2 || len(d.graphPropertyRecords) != 2 || d.graphPropertyRecords[1].Request.After.Kind != "null" {
		t.Fatal("removal retained a fake NULL property or changed its independent snapshot")
	}
	after, _ := json.Marshal(d.result)
	if !bytes.Equal(original, after) {
		t.Fatal("presence operations modified original query records")
	}
}

func TestGraphPropertyCreationLocalizedKindChangeRevokesPreview(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	i18n.SetLocaleFunc(func() string { return "en" })
	s, d, _ := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	g := d.graphVisualState()
	g.focus("n:a")
	s.openGraphPropertyCreation(d, g)
	d.graphPropertyDraft.name = "new value"
	s.previewGraphProperty(d)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 720, 520)
	tt.Frame()
	if !tt.HasText("String") || locales.Text("布尔值") != "Boolean" || locales.Text("整数") != "Integer" || locales.Text("浮点数") != "Float" {
		t.Fatal("creation kind names were not localized")
	}
	if err := tt.Click(locales.Text("图属性类型")); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Boolean"); err != nil {
		t.Fatal(err)
	}
	p := d.graphPropertyDraft
	if p.kind != "boolean" || p.text != "false" || p.preview != nil {
		t.Fatal("changing the creation kind retained a stale preview or invalid boolean")
	}
	s.previewGraphProperty(d)
	if p.preview == nil || p.preview.After.Kind != "boolean" || p.preview.After.Text != "false" {
		t.Fatal("selected boolean kind did not preview a native false property")
	}
	p.attempted, p.unresolved = true, true
	p.notice = "图属性操作缺少当前原会话确认；保留草稿和回执，不会重放"
	d.graphPropertyRecords = append(d.graphPropertyRecords, graphPropertyRecord{Request: p.preview.Clone()})
	tt.Frame()
	if rect, ok := tt.Find(locales.Text("关闭")); !ok || rect.H < 10 || rect.Y+rect.H > 508 {
		t.Fatal("retired creation pushed the modal close button outside the compact window", rect)
	}
}

func TestGraphPropertyPresenceNameKindAndExistingPropertyValidation(t *testing.T) {
	s, d, _ := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
		return propertyReceiptTest(r), nil
	})
	defer s.CloseAll()
	g := d.graphVisualState()
	g.focus("n:a")
	s.openGraphPropertyCreation(d, g)
	p := d.graphPropertyDraft
	for _, name := range []string{"", "name", "\xff"} {
		p.name = name
		s.previewGraphProperty(d)
		if p.preview != nil || p.error == "" {
			t.Fatal("invalid or already observed property name was admitted", name)
		}
	}
	p.name, p.kind, p.text = "new exact", "integer", "+1"
	s.previewGraphProperty(d)
	if p.preview != nil {
		t.Fatal("creation admitted a noncanonical integer")
	}
	p.text = "-9223372036854775808"
	s.previewGraphProperty(d)
	if p.preview == nil || p.preview.After.Text != p.text || p.preview.Before.Kind != "null" {
		t.Fatal("creation lost the exact typed integer")
	}
	p.kind = "list"
	s.previewGraphProperty(d)
	if p.preview != nil {
		t.Fatal("creation advertised an unsupported property type")
	}
}

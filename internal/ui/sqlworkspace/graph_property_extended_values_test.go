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

func TestGraphPropertyExtendedLocalizedPickerDefaultsAndPreviewRevocation(t *testing.T) {
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
	p.name = "typed property"
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1280, 800)
	for _, sample := range []struct{ kind, label string }{
		{"date", "Date"}, {"local_time", "Local time"}, {"local_datetime", "Local date and time"}, {"time", "Time with offset"},
		{"datetime", "Date and time with zone"}, {"duration", "Duration"}, {"point2d", "2D point"}, {"point3d", "3D point"},
	} {
		s.previewGraphProperty(d)
		if p.preview == nil {
			t.Fatal("old valid preview unavailable", p.error)
		}
		tt.Frame()
		if err := tt.Click(locales.Text("图属性类型")); err != nil {
			t.Fatal(err)
		}
		if err := tt.Click(sample.label); err != nil {
			t.Fatal(err)
		}
		want, _ := graphPropertyExtendedDefault(sample.kind)
		if p.kind != sample.kind || p.text != want || p.preview != nil {
			t.Fatal("typed kind change retained a preview or wrong default", sample.kind)
		}
		s.previewGraphProperty(d)
		if p.preview == nil || p.preview.Before.Kind != "null" || p.preview.After.Kind != sample.kind {
			t.Fatal("localized extended kind lost typed preview", sample.kind, p.error)
		}
	}
	for _, size := range [][2]int{{1280, 800}, {720, 520}} {
		compact := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, size[0], size[1])
		compact.Frame()
		if rect, ok := compact.Find("Close"); !ok || rect.H < 10 || rect.Y+rect.H > float32(size[1])-10 {
			t.Fatal("extended preview hid compact close button", rect)
		}
	}
}

func TestGraphPropertyExtendedDeepRequestsParkedReceiptsAndIndependentSnapshots(t *testing.T) {
	for _, sample := range []struct{ kind, before, after string }{
		{"datetime", `{"value":"2026-07-03T01:02:03.123456789+02:00","offset_seconds":7200,"zone":"Europe/Stockholm"}`, `{"value":"1890-01-01T00:00:00+00:09","offset_seconds":561,"zone":"Europe/Paris"}`},
		{"duration", `{"months":0,"days":1,"seconds":0,"nanoseconds":0}`, `{"months":-2,"days":3,"seconds":0,"nanoseconds":-1}`},
		{"point3d", `{"srid":9157,"x":1,"y":2,"z":3}`, `{"srid":4979,"x":113.5,"y":22.3,"z":-0}`},
	} {
		t.Run(sample.kind, func(t *testing.T) {
			requests := make(chan contract.GraphPropertyWriteRequest, 1)
			release := make(chan struct{})
			s, d, l := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
				requests <- r.Clone()
				<-release
				return propertyReceiptTest(r), nil
			})
			defer s.CloseAll()
			g := d.graphVisualState()
			old, err := contract.ParseGraphPropertyExtended(sample.kind, sample.before)
			if err != nil {
				t.Fatal(err)
			}
			g.applyGraphPropertyEntity(contract.GraphPropertyTarget{Kind: "node", ElementID: "a"}, contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: "a", Properties: []contract.GraphProperty{{Name: "value", Value: old}}}})
			g.focus("n:a")
			original, _ := json.Marshal(d.result)
			s.openGraphPropertyDraft(d, g, g.properties()[0])
			p := d.graphPropertyDraft
			if p == nil || p.kind != sample.kind {
				t.Fatal("native extended editing unavailable")
			}
			p.text = sample.after
			s.previewGraphProperty(d)
			if p.preview == nil || len(p.preview.After.Properties) < 2 {
				t.Fatal("extended preview flattened exact components", p.error)
			}
			active := true
			s.deps.Active = func(*Document) bool { return active }
			s.saveGraphProperty(d)
			r := <-requests
			p.request.Before.Properties[0].Value.Text = "changed before"
			p.preview.After.Properties[0].Value.Text = "changed after"
			active = false
			close(release)
			s.Consume()
			if !d.Loading() || len(d.graphPropertyRecords) != 0 {
				t.Fatal("parked extended receipt published on another profile")
			}
			active = true
			consumeUntil(t, s, func() bool { return !d.Loading() })
			if d.graphPropertyDraft != nil || l.writes.Load() != 1 || len(d.graphPropertyRecords) != 1 || !reflect.DeepEqual(d.graphPropertyRecords[0].Request, r) || !reflect.DeepEqual(g.properties()[0].Value, r.After) {
				t.Fatal("extended worker shared preview metadata or lost complete receipt")
			}
			if !s.pinResult(d) {
				t.Fatal(d.Error())
			}
			snapshot := d.snapshots[0].Document
			d.graphPropertyRecords[0].Receipt.Result.Rows[0][2].Properties[0].Value.Text = "changed after snapshot"
			if !reflect.DeepEqual(snapshot.graphPropertyRecords[0].Receipt.Result.Rows[0][2], r.After) || !reflect.DeepEqual(snapshot.graphVisualState().properties()[0].Value, r.After) {
				t.Fatal("extended snapshot shared native property components")
			}
			after, _ := json.Marshal(d.result)
			if !bytes.Equal(original, after) {
				t.Fatal("extended editing changed original query bytes")
			}
		})
	}
}

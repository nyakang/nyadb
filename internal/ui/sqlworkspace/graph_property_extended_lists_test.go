package sqlworkspace

import (
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

func extendedListNativeSamples() []extendedNativeSample {
	samples := extendedNativeSamples()
	labels := []string{"日期列表", "本地时间列表", "本地日期时间列表", "带偏移时间列表", "带时区日期时间列表", "时长列表", "二维空间点列表", "三维空间点列表"}
	for i := range samples {
		sample := &samples[i]
		array := func(text string) string {
			if sample.kind == "date" || sample.kind == "local_time" || sample.kind == "local_datetime" {
				raw, _ := json.Marshal([]string{text, text})
				return string(raw)
			}
			return "[" + text + "," + text + "]"
		}
		sample.first, sample.after = array(sample.first), array(sample.after)
		sample.kind, sample.label = "list:"+sample.kind, labels[i]
	}
	return samples
}

func TestGraphPropertyExtendedListLocalizedSelectorsCanonicalPreviewAndEmptySubtype(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		s, d, _ := propertyDocumentTest(t, func(_ context.Context, r contract.GraphPropertyWriteRequest) (contract.GraphPropertyWriteReceipt, error) {
			return propertyReceiptTest(r), nil
		})
		defer s.CloseAll()
		g := d.graphVisualState()
		g.focus("n:a")
		s.openGraphPropertyCreation(d, g)
		p := d.graphPropertyDraft
		p.name = "typed list"
		tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 720, 520)
		for i, sample := range extendedListNativeSamples() {
			tt.Frame()
			if err := tt.Click(locales.Text("图属性类型")); err != nil {
				t.Fatal(err)
			}
			tt.Key(0, ui.KeyHome)
			for range 16 + i {
				tt.Key(0, ui.KeyDown)
			}
			tt.Key(0, ui.KeyEnter)
			if p.kind != sample.kind || p.preview != nil {
				t.Fatal("extended list selection did not revoke preview", language, sample.kind, p.kind)
			}
			p.text = sample.after
			s.previewGraphProperty(d)
			want := extendedNativeValue(t, sample.kind, sample.after)
			if p.preview == nil || !reflect.DeepEqual(p.preview.After, want) {
				t.Fatal("list preview lost typed components", sample.kind, p.error)
			}
			tt.Frame()
			for _, label := range []string{"关闭", "预览属性保存", "确认新增图属性"} {
				box, ok := tt.Find(locales.Text(label))
				if !ok || box.Y < 0 || box.Y+box.H > 520 {
					t.Fatal("compact list controls clipped", language, sample.kind, label, box)
				}
			}
		}
		d.graphPropertyDraft, d.graphPropertyOpen = nil, false
		g.applyGraphPropertyEntity(contract.GraphPropertyTarget{Kind: "node", ElementID: "a"}, contract.GraphValue{Kind: "node", Node: &contract.GraphNode{ElementID: "a", Properties: []contract.GraphProperty{{Name: "empty", Value: contract.GraphValue{Kind: "list"}}}}})
		s.openGraphPropertyDraft(d, g, g.properties()[0])
		p = d.graphPropertyDraft
		tt.Frame()
		if err := tt.Click(locales.Text("图属性类型")); err != nil {
			t.Fatal(err)
		}
		tt.Key(0, ui.KeyEnd)
		tt.Key(0, ui.KeyEnter)
		p.text = extendedListNativeSamples()[7].after
		s.previewGraphProperty(d)
		if p.kind != "list:point3d" || p.preview == nil || len(p.preview.Before.Values) != 0 {
			t.Fatal("empty list did not accept explicit extended subtype", p.kind, p.error)
		}
	}
}

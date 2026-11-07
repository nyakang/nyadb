package dataworkspace

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"testing"
)

func TestElasticsearchInspectorUsesLocalizedNativeObjectTypes(t *testing.T) {
	defer i18n.SetLocaleFunc(nil)
	for _, language := range []string{"zh-Hans", "en"} {
		i18n.SetLocaleFunc(func() string { return language })
		for _, tc := range []struct{ kind, caption string }{{"索引", "Indexes"}, {"别名", "Alias"}, {"数据流", "Data Stream"}} {
			s := newInspector(InspectorDependencies{Session: func() Session { return Session{ProfileID: 7, Connected: true} }, Profile: func(int64) (connections.Profile, bool) {
				return connections.Profile{ID: 7, Kind: connections.Elasticsearch}, true
			}})
			p := s.Open(7, "original-cluster", "target", nil)
			p.info.attempted = true
			p.info.info = &contract.TableInfo{Kind: tc.kind, Engine: "Elasticsearch"}
			tt := ui.NewTester(func(c *ui.Context) { s.TableView(c, p) }, 350, 700)
			caption, title := tc.kind, "对象信息"
			if language == "en" {
				caption, title = tc.caption, "Object Information"
			}
			if !tt.HasText(caption) || !tt.HasText(title) || tt.HasText("Tables") {
				t.Fatal("native object inspector lost type or localization", language, tc.kind)
			}
			s.Close(p)
		}
	}
}

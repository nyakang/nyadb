package objectworkspace

import (
	"context"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/manticore"
	"github.com/nyakang/nyadb/internal/ui/theme"
	"github.com/nyakang/nyadb/locales"
)

func TestManticoreNativePlainStructureOriginalMissingDefinition(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_PLAIN_IT") != "1" {
		t.Skip("requires original indexer-built plain fixture")
	}
	host, port, e := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if e != nil {
		t.Fatal(e)
	}
	n, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	native, e := manticore.Open(context.Background(), 7404, connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root"}, "")
	if e != nil {
		t.Fatal(e)
	}
	defer native.Close()
	active := Session{ProfileID: 7404, Generation: 1, Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(int64) Session { return active }, Engine: func(int64) string { return "manticoresearch" }, MetadataFor: func(int64) (contract.MetadataProvider, error) { return native, nil }})
	defer s.CloseAll()
	d := s.OpenQualifiedStructure(1, native.Namespace("Manticore").Object("TABLE", "nyadb_plain"), "表")
	s.Load(d, nil, false)
	until := time.Now().Add(10 * time.Second)
	for d.loading && time.Now().Before(until) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.loading || d.err != "" || d.data == nil || d.data.Engine != "local" || d.data.Definition != "" || len(d.data.Columns) != 3 {
		t.Fatal("plain original structure", d.err)
	}
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d) }, 1280, 800)
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		for _, page := range []struct {
			n string
			p int
		}{{"fields", 0}, {"indexes", 1}, {"definition", 2}} {
			d.page = page.p
			tt.Frame()
			if page.p == 2 && (!tt.HasText("SHOW CREATE TABLE `nyadb_plain`") || !tt.HasText("[1146 / 42S02] table 'nyadb_plain' is not real-time or percolate")) {
				t.Fatal("missing native CREATE refusal", tt.Texts())
			}
			if page.p == 0 && !tt.HasText("indexed stored") {
				t.Fatal(tt.Texts())
			}
			if page.p == 1 && os.Getenv("NYADB_MANTICORE_VERSION") == "6.3.8" && !tt.HasText(locales.Text("服务器拒绝索引查询；这不表示索引为空。")) {
				t.Fatal(tt.Texts())
			}
			if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
				f, e := os.Create(filepath.Join(dir, lang+"-plain-"+page.n+".png"))
				if e != nil {
					t.Fatal(e)
				}
				e = png.Encode(f, tt.Image())
				f.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
		}
	}
}

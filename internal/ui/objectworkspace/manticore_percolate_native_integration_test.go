package objectworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestManticoreNativePercolateStoredColumnsAndMatchDefinition(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_IT") != "1" {
		t.Skip("requires owned real Manticore fixture")
	}
	host, port, err := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	cfg := connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root", QueryTimeoutSeconds: 20}
	native, err := manticore.Open(context.Background(), 7602, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { native.Close() }()
	q, err := native.ReserveQuery(1)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Release()
	table := fmt.Sprintf("nyadb_pqobjects76_%d", time.Now().UnixNano())
	if _, err = q.Execute(context.Background(), "Manticore", "CREATE TABLE "+table+" (title text, tag string) type='pq'"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		q, err := native.ReserveQuery(99)
		if err != nil {
			t.Error(err)
			return
		}
		defer q.Release()
		if _, err = q.Execute(context.Background(), "Manticore", "DROP TABLE "+table); err != nil {
			t.Error(err)
		}
	}()
	active := Session{ProfileID: 7602, Generation: 1, Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		v := active
		v.ProfileID = id
		v.Connected = native.Alive() && id == 7602
		return v
	}, Engine: func(int64) string { return "manticoresearch" }, MetadataFor: func(id int64) (contract.MetadataProvider, error) {
		if id != 7602 {
			return nil, connections.ErrInvalid
		}
		return native, nil
	}})
	defer s.CloseAll()
	d := s.OpenQualifiedStructure(1, native.Namespace("Manticore").Object("TABLE", table), "表")
	wait := func(done func() bool, consume bool) {
		t.Helper()
		until := time.Now().Add(15 * time.Second)
		for !done() && time.Now().Before(until) {
			if consume {
				s.Consume(ui.Services{})
			}
			time.Sleep(time.Millisecond)
		}
		if !done() {
			t.Fatal("native structure did not settle")
		}
	}
	load := func() {
		t.Helper()
		s.Load(d, nil, true)
		wait(func() bool { return !d.loading }, true)
		if d.err != "" || d.data == nil {
			t.Fatal(d.err)
		}
	}
	load()
	if d.data.Engine != "percolate" || len(d.data.Columns) != 4 || d.data.Columns[1].Name != "query" || !strings.Contains(d.data.Definition, "title text") || !strings.Contains(d.data.Definition, "tag string") {
		t.Fatal(d.data)
	}
	observation, ok := manticoreIndexes(d.data)
	if !ok {
		t.Fatal("original indexes observation missing")
	}
	if os.Getenv("NYADB_MANTICORE_VERSION") == "6.3.8" {
		if observation.ErrorCode != 1064 || observation.SQLState != "42000" {
			t.Fatal(observation)
		}
	} else if observation.ErrorCode != 0 || len(observation.Result.Rows) != 0 {
		t.Fatal(observation)
	}
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, d) }, 1280, 800)
	capture := func(name string) {
		t.Helper()
		tt.Frame()
		if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
			f, err := os.Create(filepath.Join(dir, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			ce := f.Close()
			if err != nil || ce != nil {
				t.Fatal(err, ce)
			}
		}
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		d.page = 0
		capture(lang + "-percolate-stored-columns")
		if !tt.HasText("query") || !tt.HasText("tags") || !tt.HasText("filters") || tt.HasText("title") || !tt.HasText(locales.Text("这些列描述已存储的查询；待匹配文档的输入字段见原生定义。")) {
			t.Fatal(tt.Texts())
		}
		if err = tt.Click(locales.Text("原生定义")); err != nil {
			t.Fatal(err)
		}
		capture(lang + "-percolate-match-definition")
		if !strings.Contains(strings.Join(tt.Texts(), "\n"), "title text") || !tt.HasText(locales.Text("此定义描述待匹配文档的输入字段；存储查询使用 id、query、tags 和 filters 列。")) {
			t.Fatal(tt.Texts())
		}
		if err = tt.Click(locales.Text("索引")); err != nil {
			t.Fatal(err)
		}
		capture(lang + "-percolate-indexes")
		if observation.ErrorCode != 0 {
			if !tt.HasText(locales.Text("服务器拒绝索引查询；这不表示索引为空。")) {
				t.Fatal(tt.Texts())
			}
		} else if !tt.HasText(locales.Text("服务器返回空索引列表")) {
			t.Fatal(tt.Texts())
		}
	}
	cache := d.data
	s.Load(d, nil, true)
	active.ProfileID = 7699
	wait(func() bool { return len(d.results) == 1 }, false)
	s.Consume(ui.Services{})
	if d.data != cache || !d.loading {
		t.Fatal("parked original structure published")
	}
	active.ProfileID = 7602
	s.Consume(ui.Services{})
	if d.loading || d.err != "" || d.data == cache {
		t.Fatal(d.err)
	}
	cache = d.data
	s.Load(d, nil, true)
	wait(func() bool { return len(d.results) == 1 }, false)
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	native, err = manticore.Open(context.Background(), 7602, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	active.Generation++
	s.Consume(ui.Services{})
	if d.loading || d.data != cache || d.err != "原 Manticore 会话已失效，请刷新表结构" {
		t.Fatal("old generation replaced original metadata", d.err)
	}
	load()
	cache = d.data
	s.Load(d, nil, true)
	wait(func() bool { return len(d.results) == 1 }, false)
	s.Cancel(d)
	s.Consume(ui.Services{})
	if d.loading || d.data != cache {
		t.Fatal("canceled structure replaced cache")
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, err := json.MarshalIndent(map[string]any{"structure": d.data, "generation": active.Generation}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "native-percolate-structure.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.Close(d)
	if d.data != nil {
		t.Fatal("closed structure retains metadata")
	}
}

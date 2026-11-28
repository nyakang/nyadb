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

func TestManticoreNativeObjectsStructureProgramParkingAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_MANTICORE_IT") != "1" {
		t.Skip("requires owned real Manticore fixture with shipped-header UDF")
	}
	host, port, e := net.SplitHostPort(os.Getenv("NYADB_MANTICORE_ENDPOINT"))
	if e != nil {
		t.Fatal(e)
	}
	n, e := strconv.Atoi(port)
	if e != nil {
		t.Fatal(e)
	}
	cfg := connections.ManticoreConfig{Host: host, Port: uint16(n), Username: "root", QueryTimeoutSeconds: 20}
	native, e := manticore.Open(context.Background(), 7201, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { native.Close() }()
	if !strings.HasPrefix(native.Version(), os.Getenv("NYADB_MANTICORE_VERSION")+" ") {
		t.Fatal(native.Version())
	}
	q, e := native.ReserveQuery(1)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Release()
	run := func(source string) {
		t.Helper()
		if _, e := q.Execute(context.Background(), "Manticore", source); e != nil {
			t.Fatal(source, e)
		}
	}
	table := fmt.Sprintf("nyadb_objects72_%d", time.Now().UnixNano())
	run("CREATE TABLE " + table + " (title text, qty integer, tag string)")
	run("CREATE FUNCTION nyadb_fixed RETURNS INT SONAME 'nyadb_fixture.so'")
	defer func() {
		cleanup, e := native.ReserveQuery(99)
		if e != nil {
			t.Error(e)
			return
		}
		defer cleanup.Release()
		for _, sql := range []string{"DROP FUNCTION nyadb_fixed", "DROP TABLE " + table} {
			if _, e := cleanup.Execute(context.Background(), "Manticore", sql); e != nil {
				t.Error(e)
			}
		}
	}()
	objects, e := native.Browse(context.Background(), native.Namespace("Manticore"))
	if e != nil {
		t.Fatal(e)
	}
	var function contract.DBObject
	for _, o := range objects {
		if o.Name == "nyadb_fixed" {
			function = o
		}
	}
	if function.Kind != "函数" {
		t.Fatal("missing native function", function)
	}
	active := Session{ProfileID: 7201, Generation: 1, Connected: true}
	s := New(Dependencies{Session: func() Session { return active }, SessionFor: func(id int64) Session {
		v := active
		v.ProfileID = id
		v.Connected = native.Alive() && id == 7201
		return v
	}, Engine: func(int64) string { return "manticoresearch" }, MetadataFor: func(id int64) (contract.MetadataProvider, error) {
		if id != 7201 {
			return nil, connections.ErrInvalid
		}
		return native, nil
	}})
	defer s.CloseAll()
	object := native.Namespace("Manticore").Object("TABLE", table)
	d := s.OpenQualifiedStructure(1, object, "表")
	program := s.OpenProgram(2, 7201, "Manticore", function)
	if d == nil || program == nil {
		t.Fatal("qualified native documents rejected")
	}
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
			t.Fatal("original native result did not settle")
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
	observation, ok := manticoreIndexes(d.data)
	if !ok {
		t.Fatal("missing native observation")
	}
	if os.Getenv("NYADB_MANTICORE_VERSION") == "6.3.8" {
		if observation.ErrorCode == 0 {
			t.Fatal("missing original rejection")
		}
	} else if observation.ErrorCode != 0 || len(observation.Result.Rows) != 0 {
		t.Fatal("expected original empty indexes", observation)
	}
	locale := "zh-Hans"
	i18n.SetLocaleFunc(func() string { return locale })
	defer i18n.SetLocaleFunc(nil)
	shown := d
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.Consume(c.Services()); s.View(c, shown) }, 1280, 800)
	capture := func(name string) {
		t.Helper()
		tt.Frame()
		if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
			f, e := os.Create(filepath.Join(dir, name+".png"))
			if e != nil {
				t.Fatal(e)
			}
			e = png.Encode(f, tt.Image())
			ce := f.Close()
			if e != nil || ce != nil {
				t.Fatal(e, ce)
			}
		}
	}
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		d.page = 1
		capture(lang + "-objects-empty-indexes")
		if observation.ErrorCode != 0 {
			if !tt.HasText(locales.Text("服务器拒绝索引查询；这不表示索引为空。")) {
				t.Fatal(tt.Texts())
			}
		} else if !tt.HasText(locales.Text("服务器返回空索引列表")) {
			t.Fatal(tt.Texts())
		}
	}
	run("INSERT INTO " + table + " (id,title,qty,tag) VALUES (1,'猫 original',7,'original')")
	run("FLUSH RAMCHUNK " + table)
	load()
	for _, lang := range []string{"zh-Hans", "en"} {
		locale = lang
		shown = d
		d.page = 0
		capture(lang + "-objects-fields")
		if lang == "en" && (tt.HasText("定义") || tt.HasText("原生定义")) {
			t.Fatal("untranslated native definition tab")
		}
		if !tt.HasText("title") || !tt.HasText("indexed stored") || tt.HasText("PRIMARY") || tt.HasText(locales.Text("不是 null")) {
			t.Fatal("native fields misrepresented", tt.Texts())
		}
		if e := tt.Click(locales.Text("索引")); e != nil {
			t.Fatal(e)
		}
		capture(lang + "-objects-indexes")
		if os.Getenv("NYADB_MANTICORE_VERSION") != "6.3.8" && (!tt.HasText("100") || !tt.HasText("qty") || !tt.HasText(locales.Text("已启用"))) {
			t.Fatal("native indexes absent", tt.Texts())
		}
		if e := tt.Click(locales.Text("原生定义")); e != nil {
			t.Fatal(e)
		}
		capture(lang + "-objects-definition")
		if !strings.Contains(strings.Join(tt.Texts(), "\n"), "CREATE TABLE") {
			t.Fatal("original table definition missing")
		}
		shown = program
		capture(lang + "-objects-function")
		if !tt.HasText("nyadb_fixture.so") || !tt.HasText("INT") || !tt.HasText(locales.Text("SHOW PLUGINS 返回的函数元数据；服务器未提供函数实现。")) || tt.HasText(locales.Text("对象定义（由服务器返回）")) {
			t.Fatal("native plugin presented as SQL definition", tt.Texts())
		}
	}
	original := program.ProgramData().Definition
	changed := program.ProgramData()
	changed.Ref.ProfileID++
	if program.Update(changed) || program.ProgramData().Definition != original {
		t.Fatal("foreign program snapshot replaced original")
	}
	// Original completed metadata stays parked until its own profile returns.
	shown = d
	cache := d.data
	s.Load(d, nil, true)
	active.ProfileID = 7202
	wait(func() bool { return len(d.results) == 1 }, false)
	s.Consume(ui.Services{})
	if d.data != cache || !d.loading {
		t.Fatal("parked metadata published to other profile")
	}
	active.ProfileID = 7201
	s.Consume(ui.Services{})
	if d.loading || d.err != "" || d.data == cache {
		t.Fatal("original parked result not consumed")
	}
	// A real physical reconnect invalidates the prior result without replacing cache.
	cache = d.data
	s.Load(d, nil, true)
	wait(func() bool { return len(d.results) == 1 }, false)
	if e = native.Close(); e != nil {
		t.Fatal(e)
	}
	native, e = manticore.Open(context.Background(), 7201, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	active.Generation++
	s.Consume(ui.Services{})
	if d.loading || d.data != cache || d.err != "原 Manticore 会话已失效，请刷新表结构" {
		t.Fatal("old result survived real recovery", d.err)
	}
	load()
	cache = d.data
	s.Load(d, nil, true)
	wait(func() bool { return len(d.results) == 1 }, false)
	s.Cancel(d)
	s.Consume(ui.Services{})
	if d.loading || d.data != cache {
		t.Fatal("cancel replaced original cache")
	}
	if dir := os.Getenv("NYADB_MANTICORE_EVIDENCE"); dir != "" {
		raw, _ := json.MarshalIndent(map[string]any{"function": program.ProgramData(), "structure": d.data, "generation": active.Generation}, "", "  ")
		if e = os.WriteFile(filepath.Join(dir, "native-objectworkspace.json"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	s.Close(d)
	s.Close(program)
	if d.data != nil || program.ProgramData().Definition != "" {
		t.Fatal("closed native documents retained metadata")
	}
}

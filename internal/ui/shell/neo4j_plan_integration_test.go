package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/ui/navigation"
	"github.com/nyakang/nyadb/locales"
)

func TestNeo4jNativePublicStructuredPlansWorkbenchEntryAndLocalizedViews(t *testing.T) {
	if os.Getenv("NYADB_NEO4J_PUBLIC_IT") != "1" {
		t.Skip("requires owned Neo4j workbench fixture")
	}
	raw, e := os.ReadFile(os.Getenv("NYADB_NEO4J_CONFIG_FILE"))
	if e != nil {
		t.Fatal("cannot read private fixture")
	}
	var f struct {
		Config                 connections.Neo4jConfig
		Secret, Nonce, Version string
	}
	if json.Unmarshal(raw, &f) != nil || len(f.Nonce) != 32 || f.Version != "5.26.21" {
		t.Fatal("invalid fixture")
	}
	u, _ := url.Parse(f.Config.URI)
	u.Path = "/neo4j"
	if f.Config.Auth == "basic" {
		u.User = url.User(f.Config.Username)
	}
	q := url.Values{"auth": {f.Config.Auth}}
	for k, v := range map[string]string{"ca_file": f.Config.CAFile, "client_cert_file": f.Config.ClientCertFile, "client_key_file": f.Config.ClientKeyFile} {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	parsed, e := connections.ParseURI(u.String())
	if e != nil {
		t.Fatal("invalid fixture URI")
	}
	parsed.Profile.Name = "Original native plan"
	s, _, profiles, _ := profileTestState(t)
	defer s.CloseMySQLSessions()
	p, e := profiles.Create(context.Background(), parsed.Profile)
	if e != nil {
		t.Fatal(e)
	}
	s.loadProfiles()
	s.selectProfileByName(p.Name)
	if e = s.runtime.Connect(context.Background(), p, f.Secret); e != nil {
		t.Fatal(e)
	}
	w := s.sessions.Current()
	w.ProfileID = p.ID
	w.Kind = connections.Neo4j
	w.Status = "已连接"
	w.Generation = s.runtime.Generation(p.ID)
	w.Databases, e = runtimeNamespaceNames(context.Background(), s, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	w.Namespaces = map[string]contract.NamespaceRef{"neo4j": s.runtime.Namespace(p.ID, "neo4j")}
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1280, 800)
	s.submitNavigation(services, navigation.DatabaseNode(p.ID, "neo4j"))
	s.openQuery()
	d := s.currentTab().query
	if d == nil {
		t.Fatal("missing Cypher document")
	}
	d.Editor.Buffer.Set("CYPHER runtime=slotted PROFILE RETURN 9223372036854775807 AS exact")
	tt.Frame()
	if e = tt.Click("Cypher 编辑器"); e != nil {
		t.Fatal(e)
	}
	tt.Key(ui.Ctrl, ui.KeyEnter)
	deadline := time.Now().Add(40 * time.Second)
	for d.Loading() && time.Now().Before(deadline) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if d.Loading() || d.Error() != "" || d.Result() == nil || d.Result().Graph.Summary.Plan == nil || !tt.HasText("ProduceResults") {
		t.Fatal("native plan parser not wired to workbench", d.Error())
	}
	original := d.Result().Graph.Clone()
	capture := func(stage, action string, labels ...string) {
		t.Helper()
		folder := os.Getenv("NYADB_NEO4J_PLAN_IMAGES")
		if folder == "" {
			return
		}
		if e = os.MkdirAll(folder, 0700); e != nil {
			t.Fatal(e)
		}
		defer i18n.SetLocaleFunc(nil)
		for _, lang := range []string{"zh-Hans", "en"} {
			i18n.SetLocaleFunc(func() string { return lang })
			for _, size := range [][2]int{{1280, 800}, {720, 520}} {
				picture := ui.NewTester(s.View, size[0], size[1])
				if e := picture.Click(locales.Text(action)); e != nil {
					t.Fatal(e)
				}
				picture.Frame()
				for _, label := range labels {
					r, ok := picture.Find(locales.Text(label))
					if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0])+1 || r.Y+r.H > float32(size[1])+1 {
						t.Fatal("workbench plan control missing or clipped", stage, lang, size, label, r)
					}
				}
				file, e := os.Create(filepath.Join(folder, fmt.Sprintf("%s-%dx%d-%s.png", lang, size[0], size[1], stage)))
				if e != nil {
					t.Fatal(e)
				}
				e = png.Encode(file, picture.Image())
				file.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	capture("plan-workbench", "结构计划", "结构计划", "原生计划", "查询结果")
	capture("plan-workbench-native", "原生计划", "复制完整原生计划", "导出完整原生计划")
	count, e := json.Marshal(d.Result().Graph)
	before, _ := json.Marshal(original)
	if e != nil || string(count) != string(before) {
		t.Fatal("plan navigation changed original native result")
	}
}

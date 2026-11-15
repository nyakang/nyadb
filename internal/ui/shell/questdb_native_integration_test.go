package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/questdb"
	"github.com/nyakang/nyadb/internal/ui/navigation"
)

func TestQuestDBNativePublicChooserVaultSQLGridStructureAndRecovery(t *testing.T) {
	if os.Getenv("NYADB_QUESTDB_IT") != "1" {
		t.Skip("requires explicitly owned official QuestDB")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_QUESTDB_CONFIG"))
	if err != nil {
		t.Fatal("missing private native configuration")
	}
	var cfg struct {
		Host, Username, PasswordFile, Image, Container string
		Port                                           uint16
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Image != "questdb/questdb@sha256:1e109276b61087010d8a3a0b033c313fcb26f6af898cbdc30f53e51cf494d0c5" {
		t.Fatal("fixture requires the pinned official product")
	}
	secret, err := os.ReadFile(cfg.PasswordFile)
	if err != nil {
		t.Fatal("missing private original credential")
	}
	for _, readOnly := range []bool{false, true} {
		mode := "native"
		if readOnly {
			mode = "read-only"
		}
		t.Run(mode, func(t *testing.T) {
			t.Logf("official public QuestDB image=%s original_container=%s mode=%s", cfg.Image, cfg.Container, mode)
			p, _ := connections.Defaults(connections.QuestDB)
			p.Name, p.ID, p.Host, p.Port, p.Username = "original QuestDB", 991, cfg.Host, cfg.Port, cfg.Username
			fixture, err := questdb.OpenSession(context.Background(), p, string(secret))
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()
			provider, _ := contract.Require[contract.QueryProvider](fixture)
			lease, err := provider.ReserveQuery(1)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			table := "public_" + strconv.FormatInt(time.Now().UnixNano(), 36)
			if _, err = lease.Execute(context.Background(), "qdb", "CREATE TABLE \""+table+"\"(id LONG,note STRING,ts TIMESTAMP,sym SYMBOL); INSERT INTO \""+table+"\" VALUES(9007199254740993,'native shared grid','2026-11-16T00:00:00.000001Z','same'),(-9,'second original row','2026-11-16T00:00:00.000001Z','same')"); err != nil {
				t.Fatal(err)
			}
			s, _, profiles, _ := profileTestState(t)
			t.Cleanup(s.CloseMySQLSessions)
			vault := &memoryCredentialVault{secrets: map[string]string{}}
			s.SetCredentialVault(vault)
			s.SetConnectionWindowHost(func() {}, func() {})
			s.openConnectionDialog()
			ct := ui.NewTester(s.ConnectionWindowView, 920, 850)
			input := func(tt *ui.Tester, label, value string) {
				t.Helper()
				if e := tt.Click(label); e != nil {
					t.Fatal(e)
				}
				tt.Command("selectAll")
				tt.Type(value)
				tt.Frame()
			}
			wait := func(tt *ui.Tester, ready func() bool) {
				t.Helper()
				deadline := time.Now().Add(30 * time.Second)
				for !ready() && time.Now().Before(deadline) {
					tt.Frame()
					time.Sleep(time.Millisecond)
				}
				tt.Frame()
				if !ready() {
					t.Fatal("original public native request did not settle")
				}
			}
			input(ct, "搜索连接类型", "QuestDB")
			if err := ct.Click("选择 QuestDB"); err != nil {
				t.Fatal(err)
			}
			if err := ct.Click("下一步"); err != nil {
				t.Fatal(err)
			}
			if s.connectionEditor.Draft().Kind != connections.QuestDB {
				t.Fatal("public chooser lost product identity")
			}
			u := url.URL{Scheme: "questdb", Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), Path: "/qdb", User: url.UserPassword(cfg.Username, string(secret))}
			if readOnly {
				u.RawQuery = "read_only=true"
			}
			if err = s.connectionEditor.ImportURI(u.String()); err != nil {
				t.Fatal(err)
			}
			input(ct, "连接名称", "public QuestDB "+mode)
			if err = ct.Click("保存密码"); err != nil {
				t.Fatal(err)
			}
			capture := func(name string, tt *ui.Tester) {
				if out := os.Getenv("NYADB_QUESTDB_SHELL_EVIDENCE"); out != "" {
					if err := os.MkdirAll(out, 0700); err != nil {
						t.Fatal(err)
					}
					writeDomainCapture(t, out, mode+"-"+name, tt)
				}
			}
			capture("editor", ct)
			if err = ct.Click("高级"); err != nil {
				t.Fatal(err)
			}
			capture("advanced", ct)
			if err = ct.Click("常规"); err != nil {
				t.Fatal(err)
			}
			if err = ct.Click("测试连接"); err != nil {
				t.Fatal(err)
			}
			wait(ct, func() bool { return !s.connectionEditor.Testing() })
			if s.connectionEditor.Error() != "" {
				t.Fatal(s.connectionEditor.Error())
			}
			if err = ct.Click("确定"); err != nil {
				t.Fatal(err)
			}
			list, err := profiles.List(context.Background())
			if err != nil || len(list) != 1 {
				t.Fatal("public configuration not saved", err)
			}
			saved := list[0]
			stored, found, err := vault.Get(saved.ID, "")
			if err != nil || !found || stored != string(secret) || saved.QuestDBOptions.ReadOnly != readOnly {
				t.Fatal("original vault or mode changed")
			}
			var services ui.Services
			tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.View(c) }, 1400, 950)
			s.selectProfileByName(saved.Name)
			tt.Frame()
			if readOnly {
				if err = tt.RightClick(saved.Name); err != nil {
					t.Fatal(err)
				}
				if err = tt.ChooseMenuItem("打开连接"); err != nil {
					t.Fatal(err)
				}
			} else if err = tt.Click("连接所选数据库"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return s.sessions.StatusFor(saved.ID) != "连接中" })
			if !s.runtime.IsConnected(saved.ID) {
				t.Fatal("native public session not published", s.sessions.Current().Error)
			}
			generation := s.runtime.Generation(saved.ID)
			if err = s.runtime.Test(context.Background(), saved, stored); err != nil || s.runtime.Generation(saved.ID) != generation {
				t.Fatal("connection check replaced original generation", err)
			}
			s.openMySQLDatabase(services, "qdb", true)
			wait(tt, func() bool { return s.sessions.Current().Pending == "" && s.sessions.Current().Cache["qdb"] != nil })
			s.submitNavigation(services, navigation.Node{Kind: "object", ProfileID: saved.ID, Database: "qdb", Object: table, Category: "表"})
			data := s.currentTab()
			if data.data == nil || data.data.Object().Catalog != "qdb" || data.data.Object().ProfileID != saved.ID {
				t.Fatal("navigator lost qualified data identity")
			}
			wait(tt, func() bool { return !data.data.Busy() })
			if data.data.Error() != "" || data.data.Page() == nil || len(data.data.Page().Rows) != 2 || data.data.Page().Rows[0][0].Value.Text != "9007199254740993" || data.data.Page().Editable == nil || *data.data.Page().Editable {
				t.Fatal("native shared grid failed", data.data.Error())
			}
			s.dataWorkspace.EditCell(services, data.data, 0, "note", contract.Cell{Kind: contract.Text, Text: "must not identify by duplicate timestamp/symbol"})
			tt.Frame()
			if data.data.HasDraft() {
				t.Fatal("nonunique native fields granted row editing")
			}
			capture("grid", tt)
			var exported bytes.Buffer
			s.SetEngineFiles(func(name string) (string, error) { return name, nil }, func(_ string, write func(io.Writer) error) error { return write(&exported) })
			s.dataWorkspace.ExportJSONL(services, data.data)
			wait(tt, func() bool { return !data.data.Busy() })
			if data.data.Error() != "" || !strings.Contains(exported.String(), "9007199254740993") {
				t.Fatal("public complete export failed", data.data.Error())
			}
			s.openMySQLStructure(services, "qdb", table)
			tt.Frame()
			structure := s.currentTab()
			wait(tt, func() bool { return structure.object != nil && structure.object.Data() != nil })
			if !strings.Contains(structure.object.Data().Definition, "CREATE TABLE") || structure.object.Data().Editable {
				t.Fatal("original full structure or identity changed")
			}
			capture("structure", tt)
			s.openQuery()
			sql := s.currentTab()
			if sql.query == nil || sql.query.ProfileID() != saved.ID || sql.query.Database() != "qdb" {
				t.Fatal("new SQL document lost its original QuestDB profile or namespace")
			}
			wait(tt, func() bool { return !sql.query.Preparing() && !sql.query.Loading() })
			statement := "SELECT id,note FROM \"" + table + "\" ORDER BY id DESC; SELECT id FROM \"" + table + "\" WHERE false"
			sql.query.Editor.Buffer.Set(statement)
			sql.query.Editor.Select(0, len(statement))
			tt.Frame()
			if err = tt.Click("运行当前或选中 SQL"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !sql.query.Loading() })
			result := sql.query.Result()
			if result == nil || len(result.Sets) != 2 || result.Sets[0].Rows[0][0].Text != "9007199254740993" || len(result.Sets[1].Rows) != 0 || s.sqlCompletionCatalog(saved.ID, "qdb").Dialect != "questdb" {
				t.Fatalf("native SQL results/dialect failed: %s dialect=%s result=%+v", sql.query.Error(), s.sqlCompletionCatalog(saved.ID, "qdb").Dialect, result)
			}
			capture("sql", tt)
			planSource := "SELECT id,note FROM \"" + table + "\" WHERE id>0 ORDER BY id LIMIT 2"
			sql.query.Editor.Buffer.Set(planSource)
			sql.query.Editor.Select(0, len(planSource))
			tt.Frame()
			if err = tt.Click("EXPLAIN"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if err = tt.Click("执行 EXPLAIN"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !sql.query.Loading() })
			if sql.query.Error() != "" || sql.query.Result() == nil {
				t.Fatal("native public plan failed", sql.query.Error())
			}
			if _, err = s.sqlParsePlan(saved.ID, *sql.query.Result()); err != nil {
				t.Fatal(err)
			}
			capture("plan", tt)
			if err = tt.Click("查询结果"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if sql.query.Result() != result {
				t.Fatal("EXPLAIN discarded the original multi-result receipt")
			}
			sql.query.Editor.Buffer.Set(statement)
			tt.Frame()
			if out := os.Getenv("NYADB_QUESTDB_SHELL_EVIDENCE"); out != "" {
				b, _ := json.MarshalIndent(struct {
					Result             *contract.SQLResult
					Page               *contract.DataPage
					Definition, Export string
				}{result, data.data.Page(), structure.object.Data().Definition, exported.String()}, "", "  ")
				if err = os.WriteFile(filepath.Join(out, mode+"-public-proof.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.disconnectMySQLProfile(saved.ID)
			tt.Frame()
			if !sql.query.NeedsRecovery() || sql.query.Editor.Buffer.String() != statement || s.runtime.IsConnected(saved.ID) {
				t.Fatal("disconnect lost original SQL or retained connection")
			}
			for _, tab := range s.tabs {
				if tab.profileID() == saved.ID && (tab.data != nil || tab.object != nil) {
					t.Fatal("disconnect retained scoped documents")
				}
			}
			s.sessions.Connect(saved, services.Invalidate)
			wait(tt, func() bool { return s.sessions.StatusFor(saved.ID) != "连接中" })
			if !s.runtime.IsConnected(saved.ID) || s.runtime.Generation(saved.ID) == generation {
				t.Fatal("original profile recovery did not publish a new generation")
			}
			if err = tt.Click("建立新物理会话（不执行现有 SQL）"); err != nil {
				t.Fatal(err)
			}
			wait(tt, func() bool { return !sql.query.NeedsRecovery() && !sql.query.Loading() })
			if sql.query.Result() != nil || sql.query.Editor.Buffer.String() != statement {
				t.Fatal("recovery replayed SQL")
			}
		})
	}
}

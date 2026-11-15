package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/services/rqlite"
	"github.com/nyakang/nyadb/internal/ui/objectworkspace"
)

// This controlled fixture exercises unpublished RQLite document contracts.
// SQLite's descriptor admits those contracts solely inside this test. The
// production RQLite descriptor and session remain read-only for schema design.
type rqliteSchemaAdapterFixture struct {
	*rqlite.Session
	t        *testing.T
	fault    bool
	observed chan contract.SchemaDesignOutcome
	release  <-chan struct{}
}

func (s *rqliteSchemaAdapterFixture) Capabilities() contract.Capabilities {
	caps := s.Session.Capabilities()
	caps.DesignTables = true
	return caps
}
func (s *rqliteSchemaAdapterFixture) SchemaDesign() contract.SchemaDesignOperations {
	ops := s.Session.SchemaDesign()
	ops.CanApply = func(target contract.SchemaDesignTarget) bool {
		return target.Generation == s.Generation() && target.Object.ProfileID == s.ProfileID() && target.Object.Catalog == "main" && target.Object.Schema == ""
	}
	ops.Apply = func(context.Context, contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
		s.t.Error("UI used a session-level write instead of its original schema lease")
		return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
	}
	return ops
}
func (s *rqliteSchemaAdapterFixture) ReserveSchemaDesignDocument(r contract.SchemaDesignDocumentRequest) (contract.SchemaDesignDocumentLease, error) {
	l, err := s.Session.ReserveSchemaDesignDocument(r)
	if err != nil {
		return nil, err
	}
	return &rqliteSchemaAdapterLease{SchemaDesignDocumentLease: l, host: s}, nil
}

type rqliteSchemaAdapterLease struct {
	contract.SchemaDesignDocumentLease
	host *rqliteSchemaAdapterFixture
}

func (l *rqliteSchemaAdapterLease) Operations() contract.SchemaDesignOperations {
	ops := l.SchemaDesignDocumentLease.Operations()
	apply := ops.Apply
	if apply != nil {
		ops.Apply = func(ctx context.Context, r contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			out := apply(ctx, r)
			l.host.observed <- out.Clone()
			<-l.host.release
			if l.host.fault {
				out.Verified = false
				out.Error = errors.New("controlled loss of verification after original native commit")
			}
			return out
		}
	}
	return ops
}

func TestRQLiteNativeSchemaDocumentAdapterRuntimeUIOriginalReceiptAndPreview(t *testing.T) {
	if os.Getenv("NYADB_RQLITE_SHELL_IT") != "1" {
		t.Skip("requires pinned official RQLite adapter/UI fixtures")
	}
	for _, fault := range []bool{false, true} {
		name := map[bool]string{false: "verified", true: "verification-rejected"}[fault]
		t.Run(name, func(t *testing.T) {
			uri := rqlitePublicFixture(t, "none")
			parsed, err := connections.ParseURI(uri)
			if err != nil {
				t.Fatal(err)
			}
			parsed.Profile.ID = 991
			observed := make(chan contract.SchemaDesignOutcome, 1)
			release := make(chan struct{})
			m := dbruntime.New()
			t.Cleanup(func() { _ = m.Close() })
			m.Register(connections.SQLite, dbruntime.ProviderFunc(func(ctx context.Context, _ connections.Profile, _ string) (contract.Session, error) {
				handle, err := rqlite.OpenSession(ctx, parsed.Profile, parsed.Password)
				if err != nil {
					return nil, err
				}
				raw := handle.(*rqlite.Session)
				if raw.Capabilities().DesignTables || raw.Capabilities().Objects || raw.SchemaDesign().Apply != nil {
					t.Error("raw RQLite schema writes were publicly published")
				}
				return &rqliteSchemaAdapterFixture{Session: raw, t: t, fault: fault, observed: observed, release: release}, nil
			}))
			published, err := m.ConnectSession(context.Background(), connections.Profile{ID: 991, Name: "Unpublished RQLite adapter fixture", Kind: connections.SQLite, Database: filepath.Join(t.TempDir(), "unused.sqlite")}, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Log("controlled SQLite capability harness exercises original native RQLite leases; this is adapter/UI evidence, not public product acceptance")
			qprovider, err := contract.Require[contract.QueryProvider](published)
			if err != nil {
				t.Fatal(err)
			}
			q, err := qprovider.ReserveQuery(100)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(q.Release)
			query := func(sql string) contract.SQLResult {
				t.Helper()
				out, err := q.Execute(context.Background(), "main", sql)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			query("CREATE TABLE native_ui_schema(id INTEGER PRIMARY KEY,v TEXT); INSERT INTO native_ui_schema VALUES(9007199254740993,'original'),(9223372036854775807,'max')")
			before := query("SELECT id,v FROM native_ui_schema ORDER BY id").Sets[0].Rows
			active := uint64(1)
			w := objectworkspace.Session{ProfileID: 991, Generation: 9, Connected: true}
			s := objectworkspace.New(objectworkspace.Dependencies{
				Session: func() objectworkspace.Session { return w }, SessionFor: func(int64) objectworkspace.Session { return w }, DocumentActive: func(id uint64) bool { return id == active },
				Engine: func(int64) string { return "sqlite" }, Capabilities: func(int64) contract.Capabilities { return published.Capabilities() },
				SchemaDesignFor: m.SchemaDesign, SchemaDocumentsFor: m.SchemaDesignDocuments, Availability: m.Availability, MetadataFor: m.Metadata, Namespace: m.Namespace,
				SchemaTarget: func(id int64, db, name string) contract.SchemaDesignTarget {
					return contract.SchemaDesignTarget{Object: m.Namespace(id, db).Object("table", name), Generation: published.Generation()}
				},
			})
			t.Cleanup(s.CloseAll)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			d := s.OpenQualifiedStructure(1, m.Namespace(991, "main").Object("table", "native_ui_schema"), "表")
			var services ui.Services
			tt := ui.NewTester(func(c *ui.Context) { services = c.Services(); s.Consume(services); s.View(c, d) }, 1200, 850)
			tt.Frame()
			wait := func(done func() bool) {
				t.Helper()
				deadline := time.Now().Add(20 * time.Second)
				for !done() && time.Now().Before(deadline) {
					tt.Frame()
					time.Sleep(time.Millisecond)
				}
				if !done() {
					t.Fatal("original native schema UI request did not settle", d.Error())
				}
				tt.Frame()
			}
			s.BeginDesign(d, services)
			wait(func() bool { return !d.DesignBusy() && tt.HasText("添加字段") })
			if d.Error() != "" {
				t.Fatal("native schema designer could not load original proof", d.Error())
			}
			if err := tt.Click("添加字段"); err != nil {
				t.Fatal(err)
			}
			version := query("PRAGMA main.schema_version").Sets[0].Rows
			if err := s.PrepareSchemaDesign(d); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(version, query("PRAGMA main.schema_version").Sets[0].Rows) || d.SchemaDesignOriginalOutcome() != nil {
				t.Fatal("native preview executed or created a receipt")
			}
			tt.Frame()
			if err := tt.Click("确认执行结构 SQL"); err != nil {
				t.Fatal(err)
			}
			active = 2
			var native contract.SchemaDesignOutcome
			select {
			case native = <-observed:
			case <-time.After(20 * time.Second):
				t.Fatal("original native write did not return")
			}
			if native.State != "committed" || !native.Verified || native.Receipt == nil || native.Receipt.Sequence != 1 {
				t.Fatal("preview consumed write sequence or native write did not commit", native.State, native.Error)
			}
			close(release)
			for i := 0; i < 3; i++ {
				tt.Frame()
			}
			if !d.DesignBusy() || d.SchemaDesignOriginalOutcome() != nil {
				t.Fatal("original receipt consumed while another document was active")
			}
			active = 1
			wait(func() bool { return !d.DesignBusy() })
			out := d.SchemaDesignOriginalOutcome()
			if out == nil || out.Receipt.NativeJSON != native.Receipt.NativeJSON || out.Receipt.Original.Target.Generation != published.Generation() || out.Verified == fault || out.State != "committed" {
				t.Fatal("UI/runtime lost original native evidence or generation", out)
			}
			if !reflect.DeepEqual(before, query("SELECT id,v FROM native_ui_schema ORDER BY id").Sets[0].Rows) {
				t.Fatal("native schema write changed original complete rows")
			}
			if fault {
				if !d.DesignDirty() || s.PrepareSchemaDesign(d) == nil || !out.Receipt.Verified {
					t.Fatal("rejected verification erased original successful receipt or admitted replay")
				}
			}
			if err := tt.Click("原始结构回执"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if err := tt.Click("复制完整回执"); err != nil {
				t.Fatal(err)
			}
			copy := tt.Clipboard()
			var record struct {
				Receipt contract.SchemaDesignWriteReceipt
			}
			if json.Unmarshal([]byte(copy), &record) != nil || !reflect.DeepEqual(record.Receipt, *out.Receipt) {
				t.Fatal("clipboard rewrote complete original receipt")
			}
			files := make(chan string, 1)
			s.SetSchemaReceiptFiles(func(string) (string, error) { return "receipt.json", nil }, func(_ string, write func(io.Writer) error) error {
				var b bytes.Buffer
				err := write(&b)
				files <- b.String()
				return err
			})
			tt.Frame()
			if err := tt.Click("导出完整回执"); err != nil {
				t.Fatal(err)
			}
			select {
			case raw := <-files:
				if raw != copy {
					t.Fatal("native receipt export differed from original clipboard")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("native original receipt export did not finish")
			}
			if evidence := os.Getenv("NYADB_RQLITE_SHELL_EVIDENCE"); evidence != "" {
				if err := os.MkdirAll(evidence, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(evidence, "schema-adapter-"+name+"-receipt.json"), []byte(copy), 0600); err != nil {
					t.Fatal(err)
				}
				f, err := os.Create(filepath.Join(evidence, "schema-adapter-"+name+".png"))
				if err != nil {
					t.Fatal(err)
				}
				err = png.Encode(f, tt.Image())
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
			}
			s.Close(d)
			if d.SchemaDesignOriginalOutcome() != nil {
				t.Fatal("closed native schema document retained original receipt")
			}
		})
	}
}

package objectworkspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func dictionaryFixture(t *testing.T) (*State, *ui.Tester, *Session, *BrowserSnapshot) {
	t.Helper()
	w := &Session{ProfileID: 7, Generation: 3, Connected: true}
	n := contract.NamespaceRef{ProfileID: 7, Catalog: "original", Schema: "aux"}
	b := &BrowserSnapshot{ProfileID: 7, Database: "aux", Connected: true, Opened: true}
	r := ObjectSearchRuntime{Browse: func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
		return []contract.DBObject{{Ref: n.Object("table", "orders"), Name: "orders", Kind: "表"}, {Ref: n.Object("table", "other"), Name: "other", Kind: "表"}}, nil
	}, TableMetadata: func(_ context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
		return &contract.TableStructure{Object: o, Name: o.Name, Columns: []contract.StructureColumn{{Name: "original_column", Type: "text", Comment: "read only"}}}, nil
	}}
	s := New(Dependencies{Namespace: func(int64, string) contract.NamespaceRef { return n }, Session: func() Session { return *w }, SessionFor: func(int64) Session { return *w }, Browser: func() BrowserSnapshot { return *b }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true} }, SearchRuntime: func(int64) (ObjectSearchRuntime, error) { return r, nil }})
	t.Cleanup(s.CloseAll)
	tt := ui.NewTester(func(c *ui.Context) {
		s.Consume(c.Services())
		ui.Button(c, "Tools").Menu(s.DictionaryMenu)
		s.Modals(c)
	}, 1280, 800)
	return s, tt, w, b
}
func waitDictionary(t *testing.T, s *State, tt *ui.Tester) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for s.dictionary != nil && s.dictionary.busy && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	tt.Frame()
	if s.dictionary == nil || s.dictionary.busy {
		t.Fatal("dictionary did not settle")
	}
}

func TestDictionaryTesterMenuPreviewFilterAndExportAtNativeSizes(t *testing.T) {
	s, tt, _, _ := dictionaryFixture(t)
	path := filepath.Join(t.TempDir(), "dictionary.md")
	s.SetDictionaryFilePicker(func(string) (string, error) { return path, nil })
	tt.Frame()
	if err := tt.Click("Tools"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("数据字典…"); err != nil {
		t.Fatal(err)
	}
	if s.dictionary == nil || s.dictionary.document != nil || s.dictionary.busy {
		t.Fatal("opening auto-read metadata")
	}
	if err := tt.Click("读取字典"); err != nil {
		t.Fatal(err)
	}
	waitDictionary(t, s, tt)
	if s.dictionary.err != "" || len(s.dictionary.document.Tables) != 2 {
		t.Fatal(s.dictionary.err)
	}
	for _, size := range [][2]int{{1280, 800}, {840, 560}} {
		tt.SetSize(size[0], size[1])
		tt.Frame()
		for _, label := range []string{"搜索字典表名", "读取字典", "导出字典…", "关闭"} {
			if _, ok := tt.Find(label); !ok {
				t.Fatalf("missing %s at %v", label, size)
			}
		}
	}
	if err := tt.Click("搜索字典表名"); err != nil {
		t.Fatal(err)
	}
	tt.Type("orders")
	if !tt.HasText("orders") || tt.HasText("other") {
		t.Fatal("search did not filter preview")
	}
	if err := tt.Click("导出字典…"); err != nil {
		t.Fatal(err)
	}
	waitDictionary(t, s, tt)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "orders") || !strings.Contains(string(data), "other") {
		t.Fatal("preview filter incorrectly limited full export", err)
	}
}

func TestDictionaryRejectsOldMenuNamespaceGenerationAndLateFileSelection(t *testing.T) {
	for _, change := range []string{"profile", "generation", "database"} {
		t.Run(change, func(t *testing.T) {
			s, tt, w, b := dictionaryFixture(t)
			tt.Frame()
			if err := tt.Click("Tools"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "profile":
				w.ProfileID = 8
				b.ProfileID = 8
			case "generation":
				w.Generation++
			case "database":
				b.Database = "main"
			}
			_ = tt.ChooseMenuItem("数据字典…")
			if s.dictionary != nil {
				t.Fatal("stale menu opened new scope")
			}
		})
	}
	s, tt, w, _ := dictionaryFixture(t)
	s.OpenDictionary(7, "aux", "orders", "table")
	tt.Frame()
	if err := tt.Click("读取字典"); err != nil {
		t.Fatal(err)
	}
	waitDictionary(t, s, tt)
	started, release := make(chan struct{}), make(chan struct{})
	path := filepath.Join(t.TempDir(), "must-not-exist.md")
	s.SetDictionaryFilePicker(func(string) (string, error) { close(started); <-release; return path, nil })
	if err := tt.Click("导出字典…"); err != nil {
		t.Fatal(err)
	}
	<-started
	w.Generation++
	tt.Frame()
	close(release)
	until := time.Now().Add(80 * time.Millisecond)
	for time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || s.dictionary != nil {
		t.Fatal("late picker wrote file or modified new UI")
	}
}

func TestDictionaryCloseCancelsCapturedWorkerAndOfflineCapabilitiesDisable(t *testing.T) {
	s, tt, w, _ := dictionaryFixture(t)
	w.Connected = false
	s.OpenDictionary(7, "aux", "orders", "table")
	if s.dictionary != nil {
		t.Fatal("offline dictionary started metadata")
	}
	w.Connected = true
	start, stopped := make(chan struct{}), make(chan struct{})
	original := s.deps.SearchRuntime
	s.deps.SearchRuntime = func(id int64) (ObjectSearchRuntime, error) {
		r, e := original(id)
		r.TableMetadata = func(ctx context.Context, o contract.ObjectRef) (*contract.TableStructure, error) {
			close(start)
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		}
		return r, e
	}
	s.OpenDictionary(7, "aux", "orders", "table")
	tt.Frame()
	if err := tt.Click("读取字典"); err != nil {
		t.Fatal(err)
	}
	<-start
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("close failed to cancel worker")
	}
	tt.Frame()
	if s.dictionary != nil {
		t.Fatal("closed response resurrected dictionary")
	}
	s.deps.Capabilities = func(int64) contract.Capabilities { return contract.Capabilities{} }
	s.OpenDictionary(7, "aux", "orders", "table")
	if s.dictionary != nil {
		t.Fatal("unsupported capability started metadata")
	}
}

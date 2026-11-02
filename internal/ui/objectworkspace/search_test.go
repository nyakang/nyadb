package objectworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func searchFixture() (ObjectSearchRuntime, contract.Namespace) {
	n := contract.Namespace{Ref: contract.NamespaceRef{ProfileID: 7, Catalog: "catalog", Schema: "public"}, Label: "public"}
	r := ObjectSearchRuntime{Snapshot: []contract.Namespace{n}, Namespaces: func(context.Context) ([]contract.Namespace, error) { return []contract.Namespace{n}, nil }, Browse: func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
		return []contract.DBObject{{Ref: n.Ref.Object("table", "Orders"), Name: "Orders", Kind: "表", Comment: "invoice records"}, {Ref: n.Ref.Object("view", "OpenOrders"), Name: "OpenOrders", Kind: "视图", Definition: "CREATE VIEW OpenOrders AS SELECT invoice_id FROM Orders"}}, nil
	}, TableMetadata: func(context.Context, contract.ObjectRef) (*contract.TableStructure, error) {
		return &contract.TableStructure{Columns: []contract.StructureColumn{{Name: "invoice_id", Comment: "invoice key"}}}, nil
	}}
	return r, n
}

func TestObjectSearchMatchesQualifiedObjectsFieldsAndBoundedDefinition(t *testing.T) {
	r, n := searchFixture()
	got := runObjectSearch(context.Background(), r, objectSearchRequest{profile: 7, query: "invoice", fields: true, definitions: true})
	if got.err != nil || len(got.entries) != 4 {
		t.Fatalf("search: %d %v", len(got.entries), got.err)
	}
	for _, e := range got.entries {
		if e.Object.Ref.Namespace() != n.Ref || e.Namespace != n {
			t.Fatal("lost qualified identity")
		}
	}
	for _, spec := range []struct {
		query, mode string
		sensitive   bool
		want        int
	}{{"ord", "prefix", false, 1}, {"Orders", "exact", true, 1}, {"orders", "exact", true, 0}, {"order", "contains", false, 2}} {
		got = runObjectSearch(context.Background(), r, objectSearchRequest{profile: 7, query: spec.query, mode: spec.mode, sensitive: spec.sensitive})
		if len(got.entries) != spec.want {
			t.Fatalf("%+v: %d", spec, len(got.entries))
		}
	}
	long := strings.Repeat("多", 5000) + "target" + strings.Repeat("文", 5000)
	excerpt := objectSearchExcerpt(long, "target", false)
	if len([]rune(excerpt)) > 514 || !strings.Contains(excerpt, "target") {
		t.Fatal("unbounded or unhelpful definition excerpt")
	}
}

func TestObjectSearchRejectsWrongIdentityAndBoundsMemoryAndErrors(t *testing.T) {
	r, n := searchFixture()
	r.Browse = func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
		return []contract.DBObject{{Ref: contract.NamespaceRef{ProfileID: 8, Catalog: "catalog"}.Object("table", "target"), Name: "target", Kind: "表"}, {Ref: n.Ref.Object("view", "target"), Name: "target", Kind: "表"}, {Ref: n.Ref.Object("table", "target"), Name: "target", Kind: "表", Definition: strings.Repeat("x", 17<<20)}}, nil
	}
	got := runObjectSearch(context.Background(), r, objectSearchRequest{profile: 7, query: "target"})
	if len(got.entries) != 0 || !errors.Is(got.err, contract.ErrRevoked) || !strings.Contains(got.err.Error(), "16 MiB") {
		t.Fatalf("wrong identity or limit: %d %v", len(got.entries), got.err)
	}
}

func TestObjectSearchTesterOfflineCapabilitySearchAndOriginalOpen(t *testing.T) {
	r, n := searchFixture()
	session := Session{ProfileID: 7, Generation: 3, Connected: true}
	opened := 0
	s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true} }, SearchRuntime: func(int64) (ObjectSearchRuntime, error) { return r, nil }, OpenSearchObject: func(_ ui.Services, p int64, g uint64, e ObjectSearchEntry) {
		if p != 7 || g != 3 || e.Object.Ref.Namespace() != n.Ref {
			t.Fatal("wrong opening target")
		}
		opened++
	}})
	defer s.CloseAll()
	session.Connected = false
	s.OpenObjectSearch(7)
	if s.search != nil {
		t.Fatal("offline search scheduled metadata")
	}
	session.Connected = true
	s.OpenObjectSearch(7)
	tt := ui.NewTester(func(c *ui.Context) { s.Consume(c.Services()); s.Modals(c) }, 1280, 800)

	tt.SetSize(1280, 800)
	tt.Frame()
	if len(s.search.namespaces) != 1 {
		t.Fatal("initial namespace snapshot missing")
	}
	if err := tt.Click("查找对象名称、注释或定义"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Orders")
	if err := tt.Click("开始查找"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for s.search.busy && time.Now().Before(until) {
		tt.Frame()
		time.Sleep(time.Millisecond)
	}
	if s.search.busy || len(s.search.entries) != 2 {
		t.Fatal("native search did not finish")
	}
	if err := tt.Click("Orders"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnter)
	if opened != 1 {
		t.Fatal("Enter did not open selected original object")
	}
}

func TestObjectSearchCloseCancelAndGenerationRevokeLateReceipts(t *testing.T) {
	for _, action := range []string{"close", "cancel", "switch", "generation"} {
		t.Run(action, func(t *testing.T) {
			r, _ := searchFixture()
			release := make(chan struct{})
			started := make(chan struct{})
			r.Browse = func(context.Context, contract.NamespaceRef) ([]contract.DBObject, error) {
				close(started)
				<-release
				return []contract.DBObject{{Name: "Orders", Kind: "表"}}, nil
			}
			session := Session{ProfileID: 7, Generation: 3, Connected: true}
			s := New(Dependencies{Session: func() Session { return session }, SessionFor: func(int64) Session { return session }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true} }, SearchRuntime: func(int64) (ObjectSearchRuntime, error) { return r, nil }})
			defer s.CloseAll()
			s.OpenObjectSearch(7)
			s.search.text = "Orders"
			s.startObjectSearch(ui.Services{})
			<-started
			receipt := s.search.results
			switch action {
			case "close":
				s.closeObjectSearch()
			case "cancel":
				s.cancelObjectSearch()
			case "switch":
				session.ProfileID = 8
				s.consumeObjectSearch()
			case "generation":
				session.Generation++
				s.consumeObjectSearch()
			}
			close(release)
			select {
			case <-receipt:
			case <-time.After(time.Second):
				t.Fatal("worker did not settle")
			}
			s.consumeObjectSearch()
			if s.search != nil && (s.search.busy || len(s.search.entries) != 0) {
				t.Fatal("revoked receipt replaced UI")
			}
		})
	}
}

func TestObjectSearchMissingRuntimeAndFieldCapabilityAreDisabled(t *testing.T) {
	runtime, _ := searchFixture()
	session := Session{ProfileID: 7, Generation: 3, Connected: true}
	s := New(Dependencies{Session: func() Session { return session }, Capabilities: func(int64) contract.Capabilities { return contract.Capabilities{Metadata: true} }, SearchRuntime: func(int64) (ObjectSearchRuntime, error) { return runtime, nil }})
	defer s.CloseAll()
	original := runtime.Browse
	runtime.Browse = nil
	if s.CanSearchObjects(7) {
		t.Fatal("missing metadata method enabled search")
	}
	runtime.Browse = original
	runtime.TableMetadata = nil
	s.OpenObjectSearch(7)
	tt := ui.NewTester(func(c *ui.Context) { s.Modals(c) }, 1280, 800)
	tt.Frame()
	if err := tt.Click("查找字段名称和注释"); err != nil {
		t.Fatal(err)
	}
	if s.search.fields || s.search.canFields {
		t.Fatal("missing field loader enabled field search")
	}
}

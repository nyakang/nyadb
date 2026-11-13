package kvworkspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func healthBrowserReceipt(count int, instances bool) *Receipt {
	r := &Receipt{Scope: testScope()}
	r.Scope.HTTP.Protocol = "consul-http"
	h := &contract.KVHTTPResult{Protocol: "HTTP/1.1", Request: contract.KVHTTPRequest{Method: "GET", Path: "/v1/health/state/any"}, Status: 200, Captured: true, EOF: true, Acknowledged: true, Succeeded: true, Health: &contract.KVHTTPHealthList{Kind: "checks", Complete: true, ProjectionComplete: true}}
	r.Result.HTTP = h
	rows := make([]any, count)
	for i := range count {
		c := contract.KVHTTPHealthCheck{Node: "original-node", CheckID: "same", Name: fmt.Sprintf("原检查 %03d", i), Status: "passing", Namespace: fmt.Sprint(i), Output: "original output", ModifyIndex: math.MaxUint64, PresentFields: []string{"Node", "CheckID", "Name", "Status", "Namespace", "Output", "ModifyIndex"}}
		if i%2 == 1 {
			c.Status = "warning"
		}
		if !instances {
			h.Health.Checks = append(h.Health.Checks, c)
			rows[i] = map[string]any{"Node": c.Node, "CheckID": c.CheckID, "Name": c.Name, "Status": c.Status, "Namespace": c.Namespace, "Output": c.Output, "ModifyIndex": c.ModifyIndex, "future": map[string]any{"null": nil, "integer": uint64(math.MaxUint64)}}
			continue
		}
		v := contract.KVHTTPHealthInstance{PresentFields: []string{"Node", "Service", "Checks"}, Node: contract.KVHTTPHealthNode{Node: "original-node", PresentFields: []string{"Node"}}, Service: contract.KVHTTPHealthService{ID: "same", Service: "原服务", Namespace: fmt.Sprint(i), Meta: map[string]string{"original": fmt.Sprint(i)}, PresentFields: []string{"ID", "Service", "Namespace", "Meta"}}, Checks: []contract.KVHTTPHealthCheck{c}}
		h.Health.Instances = append(h.Health.Instances, v)
		rows[i] = map[string]any{"Node": map[string]any{"Node": v.Node.Node}, "Service": map[string]any{"ID": v.Service.ID, "Service": v.Service.Service, "Namespace": v.Service.Namespace, "Meta": v.Service.Meta}, "Checks": []any{map[string]any{"CheckID": c.CheckID, "Node": c.Node, "ModifyIndex": c.ModifyIndex}}, "future": nil}
	}
	if instances {
		h.Health.Kind = "service-instances"
		h.Request.Path = "/v1/health/service/" + "%E5%8E%9F%E6%9C%8D%E5%8A%A1"
	}
	h.Body, _ = json.Marshal(rows)
	return r
}

func TestHTTPHealthBrowserQualifiedRowsPagingFiltersAndOriginalReplacement(t *testing.T) {
	for _, instances := range []bool{false, true} {
		r := healthBrowserReceipt(121, instances)
		before := r.Clone()
		var b httpHealthBrowserState
		b.bind(r)
		if b.pageCount() != 3 || len(b.pageIndices()) != 50 || b.selectedOriginal != -1 || !b.movePage(r, 2) || !b.selectRow(r, 2, 20) {
			t.Fatal("original rows collapsed or last page could not select")
		}
		index, ok := b.selectedIndex(r)
		if !ok || index != 120 {
			t.Fatal("selection confused same qualified ID with the first row")
		}
		raw, err := originalHealthRow(r.Result.HTTP.Body, index)
		if err != nil || !bytes.Contains(raw, []byte(`"Namespace":"120"`)) || !bytes.Contains(raw, []byte("18446744073709551615")) {
			t.Fatal("selected original JSON lost namespace, exact number or original position", err)
		}
		next := r.Clone()
		if b.selectRow(&next, 2, 0) || b.movePage(r, 3) || b.selectRow(r, 1, 0) {
			t.Fatal("stale original/page event acquired another row")
		}
		b.query = "原检查 120"
		if _, ok := b.selectedIndex(r); ok {
			t.Fatal("filter edit retained stale selection")
		}
		b.bind(r)
		if b.page != 0 || len(b.indices) != 1 || b.indices[0] != 120 || b.selectedOriginal != -1 {
			t.Fatal("Unicode search changed original order or selected implicitly")
		}
		b.query = ""
		b.status = httpHealthWarning
		b.bind(r)
		if len(b.indices) != 60 || b.indices[0] != 1 {
			t.Fatal("local check status filtering incorrect")
		}
		b.bind(&next)
		if b.selectedOriginal != -1 || b.detail || !reflect.DeepEqual(*r, before) {
			t.Fatal("replacement inherited selection or mutated complete original evidence")
		}
		b.close()
		if b.receipt != nil || b.indices != nil || b.preview != "" {
			t.Fatal("close retained original health cache")
		}
	}
}

func TestHTTPHealthBrowserMaintenanceMissingNullAndIncompleteReceipts(t *testing.T) {
	r := healthBrowserReceipt(4, false)
	r.Result.HTTP.Health.Checks[0].CheckID = "_service_maintenance:original"
	r.Result.HTTP.Health.Checks[0].Status = "critical"
	r.Result.HTTP.Health.Checks[1].PresentFields = nil
	r.Result.HTTP.Health.Checks[2].NullFields = []string{"Status"}
	r.Result.HTTP.Health.Checks[3].Status = "future-native"
	b := httpHealthBrowserState{status: httpHealthMaintenance}
	b.bind(r)
	if !reflect.DeepEqual(b.indices, []int{0}) || r.Result.HTTP.Health.Checks[0].Status != "critical" {
		t.Fatal("maintenance classification changed original status")
	}
	b.status = httpHealthOther
	b.bind(r)
	if !reflect.DeepEqual(b.indices, []int{1, 2, 3}) {
		t.Fatal("missing/null/native-other states collapsed", b.indices)
	}
	r.Result.HTTP.Health.Complete = false
	r.Result.HTTP.Metadata.ACLFiltered = new(true)
	if healthReceiptProjection(r) == nil {
		t.Fatal("valid ACL-filtered original observation hidden")
	}
	for _, mutate := range []func(*Receipt){func(r *Receipt) { r.Scope.HTTP.Protocol = "etcd" }, func(r *Receipt) { r.Result.HTTP.Request.Method = "PUT" }, func(r *Receipt) { r.Result.HTTP.Request.Path = "/v1/catalog/nodes" }, func(r *Receipt) { r.Result.HTTP.Request.Path = "/v1/health/state/" }, func(r *Receipt) { r.Result.HTTP.Acknowledged = false }, func(r *Receipt) { r.Result.HTTP.EOF = false }, func(r *Receipt) { r.Result.HTTP.Captured = false }, func(r *Receipt) { r.Result.HTTP.Status = 403 }, func(r *Receipt) { r.Result.HTTP.Health.ProjectionComplete = false }, func(r *Receipt) { r.Result.WriteOutcomeUnknown = true }} {
		copy := r.Clone()
		mutate(&copy)
		var b httpHealthBrowserState
		b.bind(&copy)
		if len(b.indices) != 0 || b.selectRow(&copy, 0, 0) {
			t.Fatal("incomplete or foreign response became a health observation")
		}
	}
}

func TestHTTPHealthRowExportCapturesCompleteOriginalJSONAndParksCompletion(t *testing.T) {
	s, d, p := httpTestState(t)
	d.scope.HTTP.Protocol = "consul-http"
	d.last = healthBrowserReceipt(53, false)
	d.last.Scope = d.scope.Clone()
	d.httpHealthBrowser = &httpHealthBrowserState{}
	b := d.httpHealthBrowser
	b.bind(d.last)
	b.movePage(d.last, 1)
	b.selectRow(d.last, 1, 2)
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true}}
	unknown := d.unknown
	want, err := originalHealthRow(d.last.Result.HTTP.Body, 52)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, exported := make(chan struct{}), make(chan struct{}), make(chan []byte, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	s.SetFiles(Files{SavePath: func(string) (string, error) { close(entered); <-release; return "original.json", nil }, WriteStream: func(_ string, write func(io.Writer) error) error {
		var out bytes.Buffer
		err := write(&out)
		exported <- out.Bytes()
		return err
	}})
	s.ExportSelectedHTTPHealthRow(d)
	<-entered
	d.Editor.Buffer.Set("later draft")
	d.last = healthBrowserReceipt(1, false)
	b.bind(d.last)
	active := false
	s.deps.Active = func(*Document) bool { return active }
	close(release)
	got := <-exported
	eventEventually(t, func() bool { return len(d.io.out) == 1 })
	s.Consume()
	if !d.FileBusy() || !bytes.Equal(got, want) {
		t.Fatal("export used replacement row or consumed parked original completion")
	}
	active = true
	consume(t, s, d)
	if d.Request().JSON != "later draft" || d.unknown != unknown || !d.hasUnknownWrites() || p.lease.executes.Load() != 0 || p.lease.connects.Load() != 0 {
		t.Fatal("local export changed request, unknown receipt or session authority")
	}
	d.last = healthBrowserReceipt(1, false)
	d.last.Scope.Namespace.ProfileID = 8
	b.bind(d.last)
	b.selectRow(d.last, 0, 0)
	if _, _, ok := s.capturedHealthRow(d); ok {
		t.Fatal("foreign profile acquired selected-row export")
	}
	if _, err := originalHealthRow([]byte(`[]`), 0); err == nil {
		t.Fatal("absent original JSON row accepted")
	}
	if _, err := originalHealthRow([]byte(`{}`), 0); err == nil {
		t.Fatal("foreign JSON shape accepted")
	}
	if _, err := originalHealthRow([]byte(`[]`), -1); err == nil {
		t.Fatal("negative original position accepted")
	}
}

func TestHTTPHealthCellsPreserveFieldPresenceAliasesAndUnsignedIndices(t *testing.T) {
	c := contract.KVHTTPHealthCheck{CheckID: "alias", PresentFields: []string{"CheckId", "Name", "Status", "ModifyIndex"}, NullFields: []string{"Status"}, ModifyIndex: math.MaxUint64}
	if healthCheckCell(c, 1) != "alias" || healthCheckCell(c, 2) != `""` || healthCheckCell(c, 3) != "null" || healthCheckCell(c, 6) != "18446744073709551615" {
		t.Fatal("original aliases, null/empty or unsigned indices changed")
	}
	c.Name = strings.Repeat("中", 1000)
	if len(healthCheckCell(c, 2)) > 259 || len(c.Name) != 3000 {
		t.Fatal("cell preview changed original long Unicode")
	}
}

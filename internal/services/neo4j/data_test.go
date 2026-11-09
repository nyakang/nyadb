package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataTestCounters struct{ fakeCounters }

// Fixed test-fixture names only. Production data reads bind all graph names.
func graphQuote(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func (dataTestCounters) NodesCreated() int         { return 0 }
func (dataTestCounters) RelationshipsCreated() int { return 0 }
func (dataTestCounters) PropertiesSet() int        { return 0 }
func (dataTestCounters) LabelsAdded() int          { return 0 }

type dataTestSummary struct{ fakeSummary }

func (dataTestSummary) Counters() nd.Counters   { return dataTestCounters{} }
func (dataTestSummary) QueryType() nd.QueryType { return nd.QueryTypeReadOnly }
func dataResultTest(keys []string, rows ...[]any) *fakeResult {
	r := &fakeResult{keys: keys, summary: dataTestSummary{fakeSummary{agent: "Neo4j/5.26.21"}}}
	for _, row := range rows {
		r.rows = append(r.rows, &nd.Record{Values: row})
	}
	return r
}
func dataFakeOpenTest(t *testing.T, s *Session, run func(context.Context, string, map[string]any) (nd.Result, error), inventory ...func() nd.Result) *atomic.Int32 {
	t.Helper()
	opens := &atomic.Int32{}
	s.openClient = func(_ context.Context, cfg connections.Neo4jConfig, secret string) (*Client, error) {
		if cfg.Database != "neo4j" || secret != "original-secret" {
			t.Error("opener lost captured original profile")
		}
		opens.Add(1)
		c, d := fakeClient(t, dataResultTest([]string{"entity"}))
		d.session.run = func(ctx context.Context, q string, p map[string]any, _ ...func(*nd.TransactionConfig)) (nd.Result, error) {
			if strings.Contains(q, "UNWIND keys(entity)") {
				if len(inventory) == 1 {
					return inventory[0](), nil
				}
				// These foundation cases model an empty property inventory observed
				// before the entity read. Dedicated cases exercise populated schemas.
				return dataResultTest([]string{"property", "types"}), nil
			}
			return run(ctx, q, p)
		}
		return c, nil
	}
	return opens
}
func dataLeaseTest(t *testing.T, s *Session, kind, name string) *dataLease {
	t.Helper()
	l, e := s.ReserveDataDocument(s.Namespace("neo4j").Object(kind, name))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*dataLease)
}
func TestNeo4jDataOriginalReservationsAndPublicCapabilityBoundary(t *testing.T) {
	s := fakeSessionTest(t)
	s.openClient = func(context.Context, connections.Neo4jConfig, string) (*Client, error) {
		t.Fatal("reservation performed I/O")
		return nil, nil
	}
	for _, o := range []contract.ObjectRef{s.Namespace("neo4j").Object("index", "name"), s.Namespace("system").Object("label", "name"), {ProfileID: 42, Catalog: "neo4j", Kind: "label"}, {ProfileID: 99, Catalog: "neo4j", Kind: "label", Name: "name"}} {
		if _, e := s.ReserveDataDocument(o); e == nil {
			t.Fatal("invalid graph target admitted")
		}
	}
	for i := 0; i < 32; i++ {
		dataLeaseTest(t, s, "label", "same")
	}
	if _, e := s.ReserveDataDocument(s.Namespace("neo4j").Object("relationship", "same")); !errors.Is(e, ErrBounds) {
		t.Fatal(e)
	}
	c := s.Capabilities()
	if !c.Documents || !c.JSONL || !c.Count || !c.FieldChunks || !c.EditableTables || c.Transactions || c.Import || c.DesignTables {
		t.Fatal("graph reads lost capabilities or enabled unimplemented mutations")
	}
	l := dataLeaseTestAfterRelease(t, s)
	r := l.Runtime()
	if !r.PageFailureRequiresReopen || !contract.BoundDataCapabilities(r).JSONL || r.Saver == nil || len(r.EditKinds) != 1 || r.EditKinds[0] != "update" || r.Importer != nil || r.Transactions.Begin != nil {
		t.Fatal("native reading confused with graph editing/SQL transactions")
	}
	_ = s.Close()
	if len(s.dataLeases) != 0 || s.Alive() {
		t.Fatal("session close retained data leases")
	}
}
func dataLeaseTestAfterRelease(t *testing.T, s *Session) *dataLease {
	s.mu.Lock()
	var first *dataLease
	for x := range s.dataLeases {
		first = x
		break
	}
	s.mu.Unlock()
	_ = first.Close()
	return dataLeaseTest(t, s, "relationship", "same")
}
func TestNeo4jDataSignedCursorBackRefreshAndExactProjection(t *testing.T) {
	s := fakeSessionTest(t)
	label := "猫\\u0060` RETURN 1 //"
	all := []nd.Node{{ElementId: "a", Labels: []string{label}, Props: map[string]any{"n": int64(9223372036854775807)}}, {ElementId: "b", Labels: []string{label}, Props: map[string]any{"n": []int64{1, 2}}}, {ElementId: "c", Labels: []string{label}, Props: map[string]any{"n": true}}}
	opens := dataFakeOpenTest(t, s, func(_ context.Context, q string, p map[string]any) (nd.Result, error) {
		if strings.Contains(q, label) || p["name"] != label || !strings.Contains(q, "ORDER BY elementId(entity)") {
			t.Error("graph name was not bound literally or data was unordered")
		}
		r := dataResultTest([]string{"entity"})
		for _, n := range all {
			if n.ElementId > p["after"].(string) {
				r.rows = append(r.rows, &nd.Record{Values: []any{n}})
			}
		}
		r.rows = r.rows[:min(len(r.rows), int(p["limit"].(int64)))]
		return r, nil
	})
	l := dataLeaseTest(t, s, "label", label)
	ctx := context.Background()
	p, e := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1})
	if e != nil || !p.HasMore || p.Rows[0][0].Text != "a" || !strings.Contains(p.Rows[0][2].Text, "9223372036854775807") || !p.LiveRead || p.Editable == nil || !*p.Editable {
		t.Fatal("native page lost identity/precision or invented writing", e)
	}
	for _, o := range []contract.DataOptions{{Offset: 1}, {FilterColumn: "element_id"}, {SortColumn: "element_id"}, {Limit: 1001}, {Object: s.Namespace("neo4j").Object("relationship", label)}} {
		if _, e = l.page(ctx, 42, "neo4j", label, o); e == nil {
			t.Fatal("unsupported semantics admitted")
		}
	}
	forged := cloneDataOptions(contract.DataOptions{Limit: 1, Offset: 1, After: p.NextCursor})
	forged.After.Values[0][0] = 'z'
	before := opens.Load()
	if _, e = l.page(ctx, 42, "neo4j", label, forged); e == nil || opens.Load() != before {
		t.Fatal("forged cursor dispatched")
	}
	second, e := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1, Offset: 1, After: p.NextCursor})
	if e != nil || second.Rows[0][0].Text != "b" {
		t.Fatal(e)
	}
	again, e := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1, After: p.CurrentCursor})
	if e != nil || again.Rows[0][0].Text != "a" {
		t.Fatal("back boundary lost", e)
	}
	other := dataLeaseTest(t, s, "label", label)
	if _, e = other.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1, Offset: 1, After: p.NextCursor}); e == nil {
		t.Fatal("cursor crossed original document")
	}
	if _, e = l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e = l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 1, Offset: 1, After: p.NextCursor}); e == nil {
		t.Fatal("pre-refresh cursor reused")
	}
	if string(p.NextCursor.Values[0]) != "a" {
		t.Fatal("caller mutation aliased retained page")
	}
}
func TestNeo4jDataCompleteFingerprintsChangedValuesAndBoundedFields(t *testing.T) {
	s := fakeSessionTest(t)
	n := nd.Node{ElementId: "a", Labels: []string{"L"}, Props: map[string]any{"long": strings.Repeat("猫", 3000), "number": int64(9223372036854775807)}}
	opens := dataFakeOpenTest(t, s, func(_ context.Context, _ string, _ map[string]any) (nd.Result, error) {
		return dataResultTest([]string{"entity"}, []any{n}), nil
	})
	l := dataLeaseTest(t, s, "label", "L")
	ctx := context.Background()
	p, e := l.page(ctx, 42, "neo4j", "L", contract.DataOptions{Limit: 1})
	if e != nil || !p.Rows[0][2].Truncated {
		t.Fatal(e)
	}
	r, e := l.row(ctx, 42, "neo4j", "L", *p.RowRefs[0])
	if e != nil || r.Snapshot.Mode != "values" || len(r.Snapshot.Original) != 4 || !strings.Contains(r.Values["properties"].Text, "9223372036854775807") {
		t.Fatal("complete typed row lost", e)
	}
	x, e := l.chunk(ctx, 42, "neo4j", "L", "properties", *p.RowRefs[0], 20, 1024)
	if e != nil || len([]rune(x.Text)) != 1024 || !x.HasMore {
		t.Fatal(e)
	}
	forged := cloneRowRef(*p.RowRefs[0])
	forged.TypedValues[0].Text = "other"
	before := opens.Load()
	if _, e = l.row(ctx, 42, "neo4j", "L", forged); e == nil || opens.Load() != before {
		t.Fatal("forged row dispatched")
	}
	forged = cloneRowRef(*p.RowRefs[0])
	forged.Values[0][10] ^= 1
	if _, e = l.row(ctx, 42, "neo4j", "L", forged); e == nil {
		t.Fatal("forged fingerprint accepted")
	}
	n.Props["number"] = int64(7)
	if _, e = l.chunk(ctx, 42, "neo4j", "L", "properties", *p.RowRefs[0], 0, 100); !errors.Is(e, contract.ErrDataConflict) {
		t.Fatal("changed entity chunk silently replaced original", e)
	}
	n.Props["long"] = strings.Repeat("x", (1<<20)+100)
	large, e := l.page(ctx, 42, "neo4j", "L", contract.DataOptions{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.row(ctx, 42, "neo4j", "L", *large.RowRefs[0]); !errors.Is(e, ErrBounds) {
		t.Fatal("oversized complete field admitted", e)
	}
	if _, e = l.chunk(ctx, 42, "neo4j", "L", "properties", *large.RowRefs[0], 0, 1024); e != nil {
		t.Fatal("bounded field read blocked by complete editing limit", e)
	}
}

type shortGraphWriter struct{}

func (shortGraphWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestNeo4jDataOriginalStreamingAndRelationshipIdentity(t *testing.T) {
	s := fakeSessionTest(t)
	rel := nd.Relationship{Id: 3, ElementId: "r", StartId: 1, EndId: 2, StartElementId: "a", EndElementId: "b", Type: "R", Props: map[string]any{"empty": "", "numbers": []int64{9223372036854775807}}}
	opens := dataFakeOpenTest(t, s, func(_ context.Context, q string, p map[string]any) (nd.Result, error) {
		if strings.Contains(q, "count(entity)") {
			return dataResultTest([]string{"count"}, []any{int64(1)}), nil
		}
		if p["after"] == "r" {
			return dataResultTest([]string{"entity"}), nil
		}
		return dataResultTest([]string{"entity"}, []any{rel}), nil
	})
	l := dataLeaseTest(t, s, "relationship", "R")
	ctx := context.Background()
	p, e := l.page(ctx, 42, "neo4j", "R", contract.DataOptions{Limit: 1})
	if e != nil || p.Rows[0][2].Text != "a" || p.Rows[0][3].Text != "b" {
		t.Fatal(e)
	}
	n, e := l.count(ctx, 42, "neo4j", "R")
	if e != nil || n != 1 {
		t.Fatal(e)
	}
	var out strings.Builder
	if e = l.jsonl(ctx, l.object, &out); e != nil {
		t.Fatal(e)
	}
	var v Value
	if json.Unmarshal([]byte(out.String()), &v) != nil || v.Relationship.StartElementID != "a" || !strings.Contains(out.String(), "9223372036854775807") {
		t.Fatal("native JSONL flattened graph identity/types")
	}
	before := opens.Load()
	if e = l.exportRows(ctx, 42, "neo4j", "R", contract.DataOptions{}, []string{"type"}, func(values []contract.DataValue) error { values[0].Text = "changed"; return io.ErrClosedPipe }); !errors.Is(e, io.ErrClosedPipe) || opens.Load() != before+1 {
		t.Fatal("sink failure replayed")
	}
	if e = l.jsonl(ctx, l.object, shortGraphWriter{}); !errors.Is(e, io.ErrShortWrite) {
		t.Fatal(e)
	}
}
func TestNeo4jDataCloseWaitsLateCapturedClientAndAuxiliaryCancelIsolated(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "L")
	late, d := fakeClient(t, dataResultTest([]string{"count"}, []any{int64(1)}))
	entered, ack := make(chan struct{}), make(chan struct{})
	s.openClient = func(ctx context.Context, _ connections.Neo4jConfig, _ string) (*Client, error) {
		close(entered)
		<-ctx.Done()
		<-ack
		return late, nil
	}
	done := make(chan error, 1)
	go func() { _, e := l.count(context.Background(), 42, "neo4j", "L"); done <- e }()
	<-entered
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned before original opener acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	close(ack)
	if e := <-done; e == nil {
		t.Fatal("late client published")
	}
	<-closed
	if late.Alive() || d.closes.Load() != 1 || d.session.runs.Load() != 0 || len(s.dataLeases) != 0 {
		t.Fatal("captured native handle retained/executed")
	}
	s = fakeSessionTest(t)
	l = dataLeaseTest(t, s, "label", "L")
	started := make(chan struct{})
	dataFakeOpenTest(t, s, func(ctx context.Context, q string, _ map[string]any) (nd.Result, error) {
		if strings.Contains(q, "count(entity)") {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return dataResultTest([]string{"entity"}, []any{nd.Node{ElementId: "a", Labels: []string{"L"}, Props: map[string]any{}}}), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, e := l.count(ctx, 42, "neo4j", "L"); done <- e }()
	<-started
	p, e := l.page(context.Background(), 42, "neo4j", "L", contract.DataOptions{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if e = <-done; e == nil {
		t.Fatal("count cancellation ignored")
	}
	if _, e = l.row(context.Background(), 42, "neo4j", "L", *p.RowRefs[0]); e != nil || l.lost {
		t.Fatal("count cancellation retired page range", e)
	}
}

func TestNeo4jDataCanceledPageDoesNotCancelCapturedCount(t *testing.T) {
	s := fakeSessionTest(t)
	l := dataLeaseTest(t, s, "label", "L")
	countStarted, pageStarted, finishCount := make(chan struct{}), make(chan struct{}), make(chan struct{})
	dataFakeOpenTest(t, s, func(ctx context.Context, q string, _ map[string]any) (nd.Result, error) {
		if strings.Contains(q, "count(entity)") {
			close(countStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-finishCount:
				return dataResultTest([]string{"count"}, []any{int64(1)}), nil
			}
		}
		close(pageStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	countDone, pageDone := make(chan error, 1), make(chan error, 1)
	go func() { _, e := l.count(context.Background(), 42, "neo4j", "L"); countDone <- e }()
	<-countStarted
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, e := l.page(ctx, 42, "neo4j", "L", contract.DataOptions{Limit: 1}); pageDone <- e }()
	<-pageStarted
	cancel()
	if e := <-pageDone; !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("page did not revoke original range", e)
	}
	select {
	case <-countDone:
		t.Fatal("page canceled independently captured count")
	default:
	}
	close(finishCount)
	if e := <-countDone; e != nil {
		t.Fatal("captured count did not finish after page revocation", e)
	}
}

package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataPoint = `{"id":18446744073709551615,"vector":{"dense":[1.00000001,0],"sparse":{"indices":[2],"values":[0.25]},"multi":[[1,0],[0,1]]},"payload":{"integer":9007199254740993,"text":"原生🐱"}}`

func unitDataLease(t *testing.T, handler http.HandlerFunc) (*Session, *dataLease) {
	t.Helper()
	srv := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/owned" {
			fmt.Fprint(w, sessionCollection)
			return
		}
		handler(w, r)
	})
	s := testSession(t, connections.QdrantConfig{Endpoint: srv.URL}, "")
	d, err := s.ReserveDataDocument(s.Namespace("owned").Object("points", "points"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return s, d.(*dataLease)
}
func dataOptions(l *dataLease) contract.DataOptions {
	return contract.DataOptions{Object: l.object, Limit: 1}
}
func TestQdrantDataExactStableProjectionFingerprintsAndOriginalReads(t *testing.T) {
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/collections/owned/points/scroll":
			fmt.Fprint(w, `{"status":"ok","result":{"points":[`+dataPoint+`],"next_page_offset":null}}`)
		case "/collections/owned/points/18446744073709551615":
			if r.URL.Query().Get("with_vector") != "true" || r.URL.Query().Get("with_payload") != "true" {
				t.Error("original projection omitted")
			}
			fmt.Fprint(w, `{"status":"ok","result":`+dataPoint+`}`)
		case "/collections/owned/points/count":
			fmt.Fprint(w, `{"status":"ok","result":{"count":1}}`)
		default:
			t.Error("unsupported request", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	o := dataOptions(l)
	page, err := l.page(context.Background(), l.object.ProfileID, l.object.Catalog, l.object.Name, o)
	if err != nil {
		t.Fatal(err)
	}
	if !page.LiveRead || !page.StableOrder || page.PagingMode != "cursor" || page.Editable == nil || *page.Editable || len(page.Rows) != 1 || len(page.Columns) != 4 || page.Rows[0][0].Value.Kind != contract.Integer || page.Rows[0][0].Text != "18446744073709551615" || !strings.Contains(page.Rows[0][2].Text, "9007199254740993") {
		t.Fatal("native projection/precision", page)
	}
	ref := *page.RowRefs[0]
	row, err := l.row(context.Background(), l.object.ProfileID, "owned", "points", ref)
	if err != nil || row.Snapshot.Mode != "values" || len(row.Snapshot.Original) != 4 || len(row.Snapshot.Fingerprints) != 0 || !strings.Contains(row.Values["vector"].Text, "1.00000001") {
		t.Fatal("original complete row", err)
	}
	chunk, err := l.chunk(context.Background(), l.object.ProfileID, "owned", "points", "point", ref, 5, 12)
	if err != nil || len([]rune(chunk.Text)) != 12 || chunk.NextOffset != 17 || !chunk.HasMore {
		t.Fatal("bounded original field", chunk, err)
	}
	if _, err = l.chunk(context.Background(), l.object.ProfileID, "owned", "points", "point", ref, 0, 1025); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded chunk accepted", err)
	}
	if count, err := l.count(context.Background(), l.object.ProfileID, "owned", "points"); err != nil || count != 1 {
		t.Fatal("native count", err)
	}
	metadata, err := l.metadata(context.Background(), l.object.ProfileID, "owned", "points")
	if err != nil || len(metadata.Columns) != 4 || !strings.Contains(metadata.Definition, "sparse_vectors") {
		t.Fatal("original collection metadata", err)
	}
	if l.Runtime().Saver != nil || l.Runtime().Capabilities.EditableTables || l.Runtime().Capabilities.Import {
		t.Fatal("unverified write exposed")
	}
	foreign := ref
	foreign.TypedValues = []contract.Cell{{Kind: contract.Text, Text: "18446744073709551615"}}
	if _, err = l.decodeRef(foreign); !errors.Is(err, ErrInvalid) {
		t.Fatal("ID type lost", err)
	}
	foreign = ref
	foreign.Values = [][]byte{bytes.Clone(ref.Values[0])}
	foreign.Values[0][len(foreign.Values[0])-2] ^= 1
	if _, err = l.decodeRef(foreign); err == nil {
		t.Fatal("tampered identity accepted")
	}
}
func TestQdrantDataCursorsBindOriginalDocumentEpochQueryAndBoundary(t *testing.T) {
	var calls atomic.Int32
	s, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/owned/points/scroll" {
			w.WriteHeader(404)
			return
		}
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var request map[string]json.RawMessage
		if decodeJSON(raw, &request) != nil {
			t.Error("invalid native request")
		}
		if _, ok := request["offset"]; ok {
			if string(request["offset"]) != "9007199254740993" {
				t.Error("native boundary changed")
			}
			fmt.Fprint(w, `{"status":"ok","result":{"points":[`+dataPoint+`],"next_page_offset":null}}`)
		} else {
			fmt.Fprint(w, `{"status":"ok","result":{"points":[{"id":0,"vector":null,"payload":null}],"next_page_offset":9007199254740993}}`)
		}
	})
	o := dataOptions(l)
	first, err := l.page(context.Background(), s.ProfileID(), "owned", "points", o)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil || first.RowSnapshots[0].Fingerprints[1] != nil || !first.Rows[0][1].Null {
		t.Fatal("null/cursor lost")
	}
	o.Offset = 1
	o.After = first.NextCursor
	second, err := l.page(context.Background(), s.ProfileID(), "owned", "points", o)
	if err != nil || second.HasMore {
		t.Fatal("native next page", err)
	}
	o.Offset = 0
	o.After = first.CurrentCursor
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "points", o); err != nil {
		t.Fatal("original back boundary", err)
	}
	otherHandle, err := s.ReserveDataDocument(l.object)
	if err != nil {
		t.Fatal(err)
	}
	defer otherHandle.Close()
	other := otherHandle.(*dataLease)
	if _, err = other.page(context.Background(), s.ProfileID(), "owned", "points", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-document cursor accepted", err)
	}
	before := calls.Load()
	o.FilterColumn = "point"
	o.FilterMode = "qdrant_filter"
	o.FilterValue = `{"must":[]}`
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "points", o); !errors.Is(err, ErrInvalid) || calls.Load() != before {
		t.Fatal("changed-filter cursor dispatched", err)
	}
	o = dataOptions(l)
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "points", o); err != nil {
		t.Fatal(err)
	}
	o.Offset = 1
	o.After = first.NextCursor
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "points", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("old epoch accepted", err)
	}
	o = dataOptions(l)
	o.Offset = 10
	if _, err = l.page(context.Background(), s.ProfileID(), "owned", "points", o); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("synthetic row skip dispatched", err)
	}
}
func TestQdrantDataFilterAndOptionsRejectSQLAndCyclesBeforeDispatch(t *testing.T) {
	filter := `{"must":[{"key":"n","match":{"value":9007199254740993}}]}`
	q, err := pointDataQuery(contract.DataOptions{FilterColumn: "point", FilterMode: "qdrant_filter", FilterValue: filter})
	if err != nil || !bytes.Contains(q.Filter, []byte("9007199254740993")) {
		t.Fatal("filter number rounded", err)
	}
	for _, o := range []contract.DataOptions{{FilterColumn: "payload.n", FilterMode: "equal", FilterValue: "1"}, {SortColumn: "id"}, {SortDescending: true}, {Filters: []contract.DataFilterRule{{Column: "point", Mode: "qdrant_filter", Value: `{"x":1,"x":2}`}}}} {
		if _, err := pointDataQuery(o); err == nil {
			t.Fatal("unsupported expression accepted")
		}
	}
	e := &contract.FilterExpression[contract.DataFilterRule]{Op: "and"}
	e.Children = append(e.Children, *e)
	e.Children[0].Children = e.Children
	if _, err = cloneDataOptions(contract.DataOptions{Condition: e}); err == nil {
		t.Fatal("cyclic condition cloned")
	}
	if validatePointFilter(`{"text":"\ud800"}`) == nil || validatePointFilter("[]") == nil {
		t.Fatal("invalid filter accepted")
	}
}
func TestQdrantDataReadConflictPreviewBoundsExportAndSinkFailure(t *testing.T) {
	var changed atomic.Bool
	var scrolls atomic.Int32
	point := strings.Replace(dataPoint, "原生🐱", strings.Repeat("猫", 2000), 1)
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/collections/owned/points/scroll":
			scrolls.Add(1)
			fmt.Fprint(w, `{"status":"ok","result":{"points":[`+point+`],"next_page_offset":null}}`)
		case "/collections/owned/points/18446744073709551615":
			read := point
			if changed.Load() {
				read = strings.Replace(read, "9007199254740993", "9007199254740995", 1)
			}
			fmt.Fprint(w, `{"status":"ok","result":`+read+`}`)
		default:
			w.WriteHeader(404)
		}
	})
	o := dataOptions(l)
	p, err := l.page(context.Background(), l.object.ProfileID, "owned", "points", o)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Rows[0][2].Truncated || len([]rune(p.Rows[0][2].Text)) != 1024 {
		t.Fatal("preview bound lost")
	}
	var out bytes.Buffer
	if err = l.jsonl(context.Background(), l.object, &out); err != nil || !bytes.Contains(out.Bytes(), []byte(strings.Repeat("猫", 2000))) {
		t.Fatal("JSONL truncated", err)
	}
	changed.Store(true)
	if _, err = l.row(context.Background(), l.object.ProfileID, "owned", "points", *p.RowRefs[0]); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed point masqueraded as original", err)
	}
	before := scrolls.Load()
	sentinel := errors.New("sink failure")
	if err = l.exportRows(context.Background(), l.object.ProfileID, "owned", "points", o, nil, func([]contract.DataValue) error { return sentinel }); !errors.Is(err, sentinel) || scrolls.Load() != before+1 {
		t.Fatal("sink failure replayed", err)
	}
	if err = l.jsonl(context.Background(), l.object, shortPointWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write hidden", err)
	}
}

type shortPointWriter struct{}

func (shortPointWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestQdrantDataCountOverflowNeverWraps(t *testing.T) {
	_, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","result":{"count":18446744073709551615}}`)
	})
	if n, err := l.count(context.Background(), l.object.ProfileID, "owned", "points"); n != 0 || !errors.Is(err, ErrBounds) {
		t.Fatal("native count wrapped into signed result", n, err)
	}
}
func TestQdrantDataReservationBoundsAndSessionCloseWaitsCapturedRead(t *testing.T) {
	started, acked := make(chan struct{}), make(chan struct{})
	s, l := unitDataLease(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/owned/points/scroll" {
			io.Copy(io.Discard, r.Body)
			close(started)
			<-r.Context().Done()
			close(acked)
			return
		}
		w.WriteHeader(404)
	})
	leases := []contract.DataDocumentLease{l}
	for range 31 {
		d, err := s.ReserveDataDocument(l.object)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, d)
	}
	if _, err := s.ReserveDataDocument(l.object); !errors.Is(err, ErrBounds) {
		t.Fatal("unbounded reservation", err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := l.page(context.Background(), s.ProfileID(), "owned", "points", dataOptions(l))
		result <- err
	}()
	waitSessionSignal(t, started)
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	waitSessionSignal(t, closed)
	waitSessionSignal(t, acked)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("captured read not canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close returned before executor")
	}
	for _, d := range leases {
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.page(context.Background(), s.ProfileID(), "owned", "points", dataOptions(l)); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed lease recovered itself", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.documents) != 0 {
		t.Fatal("document registry retained")
	}
}

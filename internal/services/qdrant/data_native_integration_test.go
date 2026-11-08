package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeData(t *testing.T) (*Session, *Client) {
	t.Helper()
	if os.Getenv("NYADB_QDRANT_DATA_IT") != "1" {
		t.Skip("requires owned Qdrant data fixture")
	}
	cfg, key, _ := nativeFixture(t)
	return testSession(t, cfg, key), nativeOpen(t)
}
func reservePointData(t *testing.T, s *Session, name string) *dataLease {
	t.Helper()
	lease, err := s.ReserveDataDocument(s.Namespace(name).Object("points", "points"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	return lease.(*dataLease)
}
func dataSeed(t *testing.T, c *Client, name string, points any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"points": points})
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Do(context.Background(), Request{Method: "PUT", Path: "/collections/" + name + "/points?wait=true", Body: body})
	if err != nil {
		t.Fatalf("native point seed HTTP %d: %s (%v)", response.Status, response.Body, err)
	}
}
func TestQdrantNativeDataExactTypedPointsCursorFilterAndOriginalFields(t *testing.T) {
	s, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"dense":{"size":2,"distance":"Dot"},"multi":{"size":2,"distance":"Dot","multivector_config":{"comparator":"max_sim"}}},"sparse_vectors":{"sparse":{}}}`)
	vector := map[string]any{"dense": []float64{1, 0}, "multi": [][]float64{{0.125, 0.25}, {0.5, 0.75}}, "sparse": map[string]any{"indices": []uint32{2, 17}, "values": []float64{0.25, 0.5}}}
	ids := []any{uint64(0), uint64(9007199254740993), uint64(18446744073709551615), "00000000-0000-0000-0000-000000000007"}
	points := make([]any, len(ids))
	for i, id := range ids {
		points[i] = map[string]any{"id": id, "vector": vector, "payload": map[string]any{"group": "first", "n": json.Number("9007199254740993"), "memo": strings.Repeat("猫", 2000), "null": nil, "nested": map[string]any{"id": "payload ID", "vector": "payload vector"}, "ordinal": i}}
	}
	dataSeed(t, c, name, points)
	l := reservePointData(t, s, name)
	o := dataOptions(l)
	var seen []string
	var first *contract.DataPage
	for {
		p, err := l.page(context.Background(), s.ProfileID(), name, "points", o)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = p
		}
		for _, row := range p.Rows {
			seen = append(seen, row[0].Text)
			if !row[2].Truncated || row[1].Value.Kind != contract.JSON {
				t.Fatal("native preview/typed payload lost")
			}
		}
		if !p.HasMore {
			break
		}
		o.Offset = p.NextCursor.Offset
		o.After = p.NextCursor
	}
	if len(seen) != 4 || seen[0] != "0" || seen[1] != "9007199254740993" || seen[2] != "18446744073709551615" || seen[3] != "00000000-0000-0000-0000-000000000007" {
		t.Fatal("native mixed ID order/precision", seen)
	}
	ref := *first.RowRefs[0]
	row, err := l.row(context.Background(), s.ProfileID(), name, "points", ref)
	if err != nil || !strings.Contains(row.Values["vector"].Text, "multi") || !strings.Contains(row.Values["vector"].Text, "sparse") || !strings.Contains(row.Values["payload"].Text, strings.Repeat("猫", 2000)) || !strings.Contains(row.Values["payload"].Text, "9007199254740993") {
		t.Fatal("complete original point read", err)
	}
	chunk, err := l.chunk(context.Background(), s.ProfileID(), name, "points", "payload", ref, 1024, 1024)
	if err != nil || len([]rune(chunk.Text)) != 1024 || !chunk.HasMore {
		t.Fatal("bounded original payload", err)
	}
	other := reservePointData(t, s, name)
	if _, err = other.row(context.Background(), s.ProfileID(), name, "points", ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign document row admitted", err)
	}
	if _, err = other.page(context.Background(), s.ProfileID(), name, "points", o); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign document cursor admitted", err)
	}
	filter := `{"must":[{"key":"ordinal","match":{"value":2}}]}`
	o = dataOptions(l)
	o.FilterColumn = "point"
	o.FilterMode = "qdrant_filter"
	o.FilterValue = filter
	page, err := l.page(context.Background(), s.ProfileID(), name, "points", o)
	if err != nil || len(page.Rows) != 1 || page.Rows[0][0].Text != "18446744073709551615" {
		t.Fatal("native filter changed", err)
	}
	count, err := l.count(context.Background(), s.ProfileID(), name, "points")
	if err != nil || count != 4 {
		t.Fatal("full native count falsely filtered", err)
	}
	o.FilterValue = `{"must":[{"key":"ordinal","match":{"value":9007199254740993}}]}`
	empty, err := l.page(context.Background(), s.ProfileID(), name, "points", o)
	if err != nil || len(empty.Rows) != 0 {
		t.Fatal("native exact filter number", err)
	}
	points[0] = map[string]any{"id": uint64(0), "vector": vector, "payload": map[string]any{"n": json.Number("9007199254740995")}}
	dataSeed(t, c, name, points[:1])
	if _, err = l.row(context.Background(), s.ProfileID(), name, "points", ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("changed native point masqueraded as original", err)
	}
}
func TestQdrantNativeDataCompleteExportBeyondUIPageLimit(t *testing.T) {
	s, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	for start := 0; start < 5015; start += 500 {
		var points []any
		for i := start; i < min(start+500, 5015); i++ {
			memo := "原生"
			if i == 7 {
				memo = strings.Repeat("猫", 2000)
			}
			points = append(points, map[string]any{"id": i, "vector": []float64{1, 0}, "payload": map[string]any{"n": json.Number("9007199254740993"), "memo": memo, "pick": i%2 == 0}})
		}
		dataSeed(t, c, name, points)
	}
	l := reservePointData(t, s, name)
	p, err := l.page(context.Background(), s.ProfileID(), name, "points", dataOptions(l))
	if err != nil || len(p.Rows) != 1 {
		t.Fatal(err)
	}
	cursor := p.NextCursor
	var out bytes.Buffer
	if err = l.jsonl(context.Background(), l.object, &out); err != nil {
		t.Fatal("complete native JSONL", err)
	}
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'})
	if len(lines) != 5015 || !bytes.Contains(lines[7], []byte(strings.Repeat("猫", 2000))) || !bytes.Contains(lines[5014], []byte("9007199254740993")) {
		t.Fatal("export truncated page/point/number", len(lines))
	}
	seen := map[string]bool{}
	for _, line := range lines {
		fields, err := object(line)
		if err != nil || seen[string(fields["id"])] || len(fields["payload"]) == 0 || len(fields["vector"]) == 0 {
			t.Fatal("native point JSONL corrupted")
		}
		seen[string(fields["id"])] = true
	}
	o := dataOptions(l)
	o.FilterColumn = "point"
	o.FilterMode = "qdrant_filter"
	o.FilterValue = `{"must":[{"key":"pick","match":{"value":true}}]}`
	rows := 0
	if err = l.exportRows(context.Background(), s.ProfileID(), name, "points", o, []string{"id", "payload"}, func(values []contract.DataValue) error {
		rows++
		if values[0].Kind != contract.Integer || !strings.Contains(values[1].Text, "9007199254740993") {
			return ErrProtocol
		}
		return nil
	}); err != nil || rows != 2508 {
		t.Fatal("complete filtered table export", rows, err)
	}
	o = dataOptions(l)
	o.Offset = cursor.Offset
	o.After = cursor
	if _, err = l.page(context.Background(), s.ProfileID(), name, "points", o); err != nil {
		t.Fatal("export invalidated page cursor", err)
	}
	if err = l.jsonl(context.Background(), l.object, shortPointWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("native short sink hidden", err)
	}
}

func TestQdrantNativeDataLargePointsExplicitBoundsAndFullExport(t *testing.T) {
	s, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	memo := strings.Repeat("x", (1<<20)+1024)
	dataSeed(t, c, name, []any{map[string]any{"id": 0, "vector": []float64{1, 0}, "payload": map[string]any{"memo": memo, "null": nil, "empty": ""}}})
	l := reservePointData(t, s, name)
	page, err := l.page(context.Background(), s.ProfileID(), name, "points", dataOptions(l))
	if err != nil {
		t.Fatal(err)
	}
	if !page.Rows[0][3].Truncated || len([]rune(page.Rows[0][3].Text)) != 1024 {
		t.Fatal("large point preview silently unbounded")
	}
	ref := *page.RowRefs[0]
	if _, err = l.row(context.Background(), s.ProfileID(), name, "points", ref); !errors.Is(err, ErrBounds) {
		t.Fatal("complete row over 1 MiB silently clipped", err)
	}
	chunk, err := l.chunk(context.Background(), s.ProfileID(), name, "points", "payload", ref, 1024, 1024)
	if err != nil || len(chunk.Text) != 1024 || !chunk.HasMore {
		t.Fatal("explicit large point chunk failed", err)
	}
	var out bytes.Buffer
	if err = l.jsonl(context.Background(), l.object, &out); err != nil || !bytes.Contains(out.Bytes(), []byte(memo)) {
		t.Fatal("complete large point JSONL lost bytes", err)
	}
}
func TestQdrantNativeDataDeletedCursorBoundaryAndObservationChanges(t *testing.T) {
	s, c := nativeData(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	dataSeed(t, c, name, []any{map[string]any{"id": 0, "vector": []float64{1, 0}}, map[string]any{"id": 1, "vector": []float64{1, 0}}, map[string]any{"id": 2, "vector": []float64{1, 0}}})
	l := reservePointData(t, s, name)
	o := dataOptions(l)
	first, err := l.page(context.Background(), s.ProfileID(), name, "points", o)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.SubmitPoints(context.Background(), name, "delete", []byte(`{"points":[1]}`), true); err != nil {
		t.Fatal(err)
	}
	o.Offset = first.NextCursor.Offset
	o.After = first.NextCursor
	next, err := l.page(context.Background(), s.ProfileID(), name, "points", o)
	if err != nil || len(next.Rows) != 1 || next.Rows[0][0].Text != "2" {
		t.Fatal("deleted native inclusive boundary skipped survivor", err)
	}
	alias := name + "_alias"
	nativeRun(t, c, Request{Method: "POST", Path: "/collections/aliases", Body: []byte(`{"actions":[{"create_alias":{"collection_name":"` + name + `","alias_name":"` + alias + `"}}]}`)})
	a := reservePointData(t, s, alias)
	if _, err = a.page(context.Background(), s.ProfileID(), alias, "points", dataOptions(a)); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("alias became physical data range", err)
	}
	nativeRun(t, c, Request{Method: "PATCH", Path: "/collections/" + name, Body: []byte(`{"optimizers_config":{"indexing_threshold":12345}}`)})
	if _, err = l.page(context.Background(), s.ProfileID(), name, "points", dataOptions(l)); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("changed physical config not rejected", err)
	}
	fresh := reservePointData(t, s, name)
	if _, err = fresh.page(context.Background(), s.ProfileID(), name, "points", dataOptions(fresh)); err != nil {
		t.Fatal("explicit new data reservation", err)
	}
}
func TestQdrantNativeDataReadPermissionsAndDeniedWrites(t *testing.T) {
	s, admin := nativeData(t)
	cfg, key, readonly := nativeFixture(t)
	if cfg.Auth == "api_key" {
		key = readonly
		s = testSession(t, cfg, key)
	}
	name := nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"}}`)
	dataSeed(t, admin, name, []any{map[string]any{"id": 1, "vector": []float64{1, 0}, "payload": map[string]any{"n": json.Number("9007199254740993")}}})
	l := reservePointData(t, s, name)
	r := l.Runtime()
	if r.Saver == nil || r.Verify == nil || r.Importer == nil || r.Transactions.Begin != nil || !r.Capabilities.EditableTables || !r.Capabilities.Import {
		t.Fatal("point runtime lacks verified writes/import or exposes transactions")
	}
	p, err := l.page(context.Background(), s.ProfileID(), name, "points", dataOptions(l))
	if err != nil || len(p.Rows) != 1 {
		t.Fatal("readonly key cannot page", err)
	}
	if _, err = l.row(context.Background(), s.ProfileID(), name, "points", *p.RowRefs[0]); err != nil {
		t.Fatal("readonly key cannot inspect", err)
	}
	if n, err := l.count(context.Background(), s.ProfileID(), name, "points"); err != nil || n != 1 {
		t.Fatal("readonly key cannot count", err)
	}
	var out bytes.Buffer
	if err = l.jsonl(context.Background(), l.object, &out); err != nil || out.Len() == 0 {
		t.Fatal("readonly key cannot export", err)
	}
	if cfg.Auth == "api_key" {
		result, writeErr := r.Saver(context.Background(), s.ProfileID(), name, "points", []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}})
		if writeErr == nil || result == nil || result.Async == nil || !result.CommitUncertain || result.Async.ReceiptJSON == "" {
			t.Fatal("readonly grid denial discarded original receipt", writeErr)
		}
		v, verifyErr := r.Verify(context.Background(), s.ProfileID(), name, "points", result)
		if verifyErr != nil || v.Status != "indeterminate" || v.Recovered.CommitConfirmed {
			t.Fatal("readonly denied write claimed commit", verifyErr)
		}
	}
	if cfg.Auth == "api_key" {
		c, err := Open(context.Background(), cfg, key)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err = c.Do(context.Background(), Request{Method: "PUT", Path: "/collections/" + name + "/points?wait=true", Body: []byte(`{"points":[{"id":2,"vector":[1,0]}]}`)}); err == nil {
			t.Fatal("native readonly key permitted write")
		}
	}
}
func TestQdrantNativeDataLostCanceledReadAndOriginalClose(t *testing.T) {
	_, admin := nativeData(t)
	name := nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"}}`)
	dataSeed(t, admin, name, []any{map[string]any{"id": 1, "vector": []float64{1, 0}, "payload": map[string]any{"n": json.Number("9007199254740993")}}})
	for _, operation := range []string{"page", "count", "field", "export"} {
		for _, fault := range []string{"lost", "canceled", "close"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				var enabled atomic.Bool
				var dispatched atomic.Int32
				observed, acked := make(chan struct{}), make(chan struct{})
				relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
					response, err := admin.Do(r.Context(), Request{Method: r.Method, Path: strings.TrimPrefix(r.URL.RequestURI(), "/gateway/qdrant"), Body: raw})
					if err != nil && response.Status == 0 {
						w.WriteHeader(502)
						return
					}
					match := strings.HasSuffix(r.URL.Path, "/points/scroll")
					if operation == "count" {
						match = strings.HasSuffix(r.URL.Path, "/points/count")
					}
					if operation == "field" {
						match = strings.HasSuffix(r.URL.Path, "/points/1")
					}
					if enabled.Load() && match {
						dispatched.Add(1)
						close(observed)
						if fault == "lost" {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
							} else {
								conn.Close()
							}
							close(acked)
							return
						}
						<-r.Context().Done()
						close(acked)
						return
					}
					w.Header().Set("Content-Type", response.ContentType)
					w.WriteHeader(response.Status)
					w.Write(response.Body)
				}))
				defer relay.Close()
				ca := filepath.Join(t.TempDir(), "relay.pem")
				if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
					t.Fatal(err)
				}
				s := testSession(t, connections.QdrantConfig{Endpoint: relay.URL + "/gateway/qdrant", CAFile: ca, ServerName: "example.com"}, "")
				l := reservePointData(t, s, name)
				o := dataOptions(l)
				page, err := l.page(context.Background(), s.ProfileID(), name, "points", o)
				if err != nil {
					t.Fatal(err)
				}
				enabled.Store(true)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					var err error
					switch operation {
					case "page":
						_, err = l.page(ctx, s.ProfileID(), name, "points", o)
					case "count":
						_, err = l.count(ctx, s.ProfileID(), name, "points")
					case "field":
						_, err = l.chunk(ctx, s.ProfileID(), name, "points", "payload", *page.RowRefs[0], 0, 1024)
					case "export":
						err = l.jsonl(ctx, l.object, io.Discard)
					}
					result <- err
				}()
				waitSessionSignal(t, observed)
				if fault == "canceled" {
					cancel()
				}
				if fault == "close" {
					l.Close()
				}
				select {
				case err := <-result:
					if err == nil || !errors.Is(err, ErrRetired) {
						t.Fatal("interrupted original read claimed success", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("original data executor did not settle")
				}
				waitSessionSignal(t, acked)
				if dispatched.Load() != 1 {
					t.Fatal("native read retried itself")
				}
				l.Close()
				if n, _, err := admin.Count(context.Background(), name, nil); err != nil || n != 1 {
					t.Fatal("independent native read damaged", err)
				}
			})
		}
	}
}

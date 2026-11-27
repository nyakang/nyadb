package databend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestDatabendSchemaTruncateLostReplyConcurrentIdentityAndNewRows(t *testing.T) {
	f := managedNativeFixture(t)
	var dispatches atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		target := req.Method == "POST" && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("TRUNCATE TABLE")) && bytes.Contains(raw, []byte("truncate_"))
		if target {
			dispatches.Add(1)
		}
		if target && bytes.Contains(raw, []byte("truncate_moved62")) {
			localNativeQuery(t, f.localTransportFixture, "ALTER TABLE \""+f.Database+"\".truncate_moved62 RENAME TO truncate_saved62")
			localNativeQuery(t, f.localTransportFixture, "CREATE TABLE \""+f.Database+"\".truncate_moved62 LIKE \""+f.Database+"\".truncate_saved62 AS SELECT * FROM \""+f.Database+"\".truncate_saved62")
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if target && bytes.Contains(raw, []byte("truncate_lost62")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		if target && bytes.Contains(raw, []byte("truncate_appended62")) {
			localNativeQuery(t, f.localTransportFixture, "INSERT INTO \""+f.Database+"\".truncate_appended62 VALUES(7)")
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 832, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	for _, name := range []string{"truncate_lost62", "truncate_moved62", "truncate_appended62"} {
		providerQuery(t, q, f.Database, "CREATE TABLE "+name+"(id UInt64)")
		providerQuery(t, q, f.Database, "INSERT INTO "+name+" VALUES(1),(2)")
		lease, snapshot := nativeSchemaDocument(t, s, "lease-"+name, name)
		request := snapshot.Clone()
		start := dispatches.Load()
		out := lease.Operations().Truncate(context.Background(), request)
		state := "committed"
		var count int64 = 0
		if name == "truncate_lost62" {
			state = "uncertain"
			count = 0
		}
		if out.State != state || out.Verified || out.Rows != count || out.Receipt == nil || out.Receipt.Rows != count || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != start+1 {
			t.Fatal("unverified clear was accepted or replayed", name, out.State, out.Rows, out.Error, dispatches.Load())
		}
		if name == "truncate_lost62" {
			localNativeQuery(t, f.localTransportFixture, "INSERT INTO \""+f.Database+"\".truncate_lost62 VALUES(7)")
		}
		rows := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM \""+f.Database+"\"."+name+" ORDER BY id")
		if name == "truncate_moved62" {
			if len(rows) != 0 {
				t.Fatal("replacement not truncated", rows)
			}
		} else if !reflect.DeepEqual(rows, [][]string{{"7"}}) {
			t.Fatal("new concurrent rows missing", rows)
		}
		var saved [][]string
		if name == "truncate_moved62" {
			saved = localNativeQuery(t, f.localTransportFixture, "SELECT id FROM \""+f.Database+"\".truncate_saved62 ORDER BY id")
			if !reflect.DeepEqual(saved, [][]string{{"1"}, {"2"}}) {
				t.Fatal("moved original changed", saved)
			}
		}
		original := lease.OriginalOutcome()
		_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
		if retry := lease.Operations().Truncate(context.Background(), request); retry.State != "not_executed" || !errors.Is(retry.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != start+1 {
			t.Fatal("unknown truncate replayed", retry)
		}
		if !reflect.DeepEqual(original, lease.OriginalOutcome()) {
			t.Fatal("read/rejected replay rewrote original")
		}
		after := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM \""+f.Database+"\"."+name+" ORDER BY id")
		if !reflect.DeepEqual(rows, after) {
			t.Fatal("replay deleted later rows", rows, after)
		}
		writeDataEvidence(t, f, "schema-"+name+".json", map[string]any{"original": original, "dispatches": dispatches.Load() - start, "independent_remaining": rows, "saved_original": saved, "read_did_not_settle": true, "replay_rejected": true, "later_rows_preserved": true})
	}
}

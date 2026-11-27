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

func TestDatabendSchemaCopyLostReplyMultiplicityAndConcurrentIdentity(t *testing.T) {
	f := managedNativeFixture(t)
	var dispatches atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		target := req.Method == "POST" && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("CREATE TABLE")) && bytes.Contains(raw, []byte(" LIKE "))
		if target {
			dispatches.Add(1)
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if target && bytes.Contains(raw, []byte("copy_lost59")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		if target && bytes.Contains(raw, []byte("copy_counts59")) {
			// Keep row count and DISTINCT values identical, but change multiplicities.
			localNativeQuery(t, f.localTransportFixture, "DELETE FROM \""+f.Database+"\".copy_counts59_dest")
			localNativeQuery(t, f.localTransportFixture, "INSERT INTO \""+f.Database+"\".copy_counts59_dest VALUES(1),(2),(2)")
		}
		if target && bytes.Contains(raw, []byte("copy_source_moved59")) {
			localNativeQuery(t, f.localTransportFixture, "ALTER TABLE \""+f.Database+"\".copy_source_moved59 RENAME TO copy_original_moved59")
			localNativeQuery(t, f.localTransportFixture, "CREATE TABLE \""+f.Database+"\".copy_source_moved59 LIKE \""+f.Database+"\".copy_original_moved59 AS SELECT * FROM \""+f.Database+"\".copy_original_moved59")
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 822, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	for _, name := range []string{"copy_lost59", "copy_counts59", "copy_source_moved59"} {
		providerQuery(t, q, f.Database, "CREATE TABLE "+name+"(id UInt64)")
		providerQuery(t, q, f.Database, "INSERT INTO "+name+" VALUES(1),(1),(2)")
		lease, snapshot := nativeSchemaDocument(t, s, "lease-"+name, name)
		request := contract.SchemaCopyRequest{Original: snapshot, Destination: name + "_dest", WithData: true}
		start := dispatches.Load()
		out := lease.Operations().Copy(context.Background(), request)
		state := "committed"
		if name == "copy_lost59" {
			state = "uncertain"
		}
		if out.State != state || out.Verified || out.Receipt == nil || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != start+1 {
			t.Fatal("unverified copy was accepted or replayed", name, out.State, out.Error, dispatches.Load())
		}
		rows := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM \""+f.Database+"\"."+name+"_dest ORDER BY id")
		expected := [][]string{{"1"}, {"1"}, {"2"}}
		if name == "copy_counts59" {
			expected = [][]string{{"1"}, {"2"}, {"2"}}
		}
		if !reflect.DeepEqual(rows, expected) {
			t.Fatal("independent original target differs", rows)
		}
		original := lease.OriginalOutcome()
		_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
		if retry := lease.Operations().Copy(context.Background(), request); retry.State != "not_executed" || !errors.Is(retry.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != start+1 {
			t.Fatal("unknown copy replayed", retry)
		}
		if !reflect.DeepEqual(original, lease.OriginalOutcome()) {
			t.Fatal("read or rejected replay rewrote original")
		}
		writeDataEvidence(t, f, "schema-"+name+".json", map[string]any{"original": original, "dispatches": dispatches.Load() - start, "independent_destination": rows, "read_did_not_settle": true, "replay_rejected": true, "destination_preserved": true})
	}
}

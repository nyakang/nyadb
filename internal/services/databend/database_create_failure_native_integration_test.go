package databend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestDatabendDatabaseCreateLostReplyOccupiedNameAndReplacement(t *testing.T) {
	f := managedNativeFixture(t)
	var dispatches atomic.Int32
	var replaced atomic.Bool
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		post := req.Method == "POST" && req.URL.Path == "/v1/query"
		create := post && bytes.Contains(raw, []byte("CREATE DATABASE")) && !bytes.Contains(raw, []byte("SHOW CREATE DATABASE")) && bytes.Contains(raw, []byte("create_"))
		if create {
			dispatches.Add(1)
		}
		if create && bytes.Contains(raw, []byte("create_occupied61")) {
			localNativeQuery(t, f.localTransportFixture, "CREATE DATABASE create_occupied61")
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if create && bytes.Contains(raw, []byte("create_lost61")) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		if post && bytes.Contains(raw, []byte("SHOW CREATE DATABASE")) && bytes.Contains(raw, []byte("create_changed61")) && replaced.CompareAndSwap(false, true) {
			localNativeQuery(t, f.localTransportFixture, "DROP DATABASE create_changed61")
			localNativeQuery(t, f.localTransportFixture, "CREATE DATABASE create_changed61")
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 902, filepath.Join(f.Directory, t.Name(), "store")))
	for _, name := range []string{"create_lost61", "create_occupied61", "create_changed61"} {
		d := nativeDatabaseCreateDocument(t, s, "lease-"+name, name)
		before, err := d.Snapshot(context.Background())
		if err != nil || before.Exists {
			t.Fatal("original absence", before, err)
		}
		start := dispatches.Load()
		out := d.Create(context.Background(), before)
		if out.Verified || out.Error == nil || out.Receipt == nil || dispatches.Load() != start+1 {
			t.Fatal("native fault accepted or replayed", name, out)
		}
		if name == "create_changed61" && out.State != "committed" || name == "create_lost61" && out.State != "uncertain" || name == "create_occupied61" && out.State == "committed" {
			t.Fatal("original acknowledgement relabelled", name, out.State)
		}
		localNativeQuery(t, f.localTransportFixture, "CREATE TABLE "+quoteName(name)+".later (id UInt64)")
		localNativeQuery(t, f.localTransportFixture, "INSERT INTO "+quoteName(name)+".later VALUES(7)")
		retained := d.OriginalOutcome()
		after, err := d.Snapshot(context.Background())
		if err != nil || !after.Exists {
			t.Fatal("independent later read", name, after, err)
		}
		if retry := d.Create(context.Background(), before); retry.State != "not_executed" || retry.Receipt != nil || dispatches.Load() != start+1 {
			t.Fatal("failed create replayed", name, retry)
		}
		if !reflect.DeepEqual(retained, d.OriginalOutcome()) {
			t.Fatal("read rewrote original")
		}
		rows := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM "+quoteName(name)+".later")
		if !reflect.DeepEqual(rows, [][]string{{"7"}}) {
			t.Fatal("later rows changed", rows)
		}
		writeDataEvidence(t, f, "database-"+name+".json", map[string]any{"original": retained, "later": after, "dispatches": dispatches.Load() - start, "independent_rows": rows, "read_did_not_settle": true, "replay_rejected": true})
	}
}

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestDatabendSchemaOriginalDropIdentityViewsAndRecreation(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 811, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	name := `drop_%猫"table`
	object := s.Namespace("").Object("table", name)
	providerQuery(t, q, f.Database, "CREATE TABLE "+qualified(object)+"(id UInt64 NOT NULL, v Binary)")
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" VALUES(18446744073709551615,unhex('00ff80'))")
	providerQuery(t, q, f.Database, "CREATE VIEW drop_dependent58 AS SELECT * FROM "+qualified(object))
	lease, snapshot := nativeSchemaDocument(t, s, "original-drop", name)
	sibling, _ := nativeSchemaDocument(t, s, "sibling-drop", name)
	if out := sibling.Operations().Drop(context.Background(), snapshot); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("sibling drop proof admitted", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := lease.Operations().Drop(ctx, snapshot); out.State != "not_executed" || out.Error == nil {
		t.Fatal("pre-canceled DROP dispatched", out)
	}
	out := lease.Operations().Drop(context.Background(), snapshot)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || out.Actual.Schema.Exists || out.Actual.Target != snapshot.Target || out.Receipt == nil || out.Receipt.Operation != "drop" || out.Receipt.Sequence != 1 || out.Actual.Schema.Databend.TableID != snapshot.Schema.Databend.TableID {
		t.Fatal("native DROP acknowledgement missing", out.State, out.Error)
	}
	rows := localNativeQuery(t, f.localTransportFixture, "SELECT name FROM system.tables WHERE database="+quoteText(f.Database)+" AND (name="+quoteText(name)+" OR name='drop_dependent58') ORDER BY name")
	if len(rows) != 1 || rows[0][0] != "drop_dependent58" {
		t.Fatal("native DROP view dependency behavior differs", rows)
	}
	if _, err := q.Execute(context.Background(), f.Database, "SELECT * FROM drop_dependent58"); err == nil {
		t.Fatal("dependent view unexpectedly remained queryable")
	}
	// The original JDBC agent may quarantine a failed query session, including
	// when its native query ID is interpreted as a connection SQLSTATE. Keep
	// that disposition intact and use an explicitly new lease for recreation.
	q.Release()
	q = reserveNativeQuery(t, s, 2)
	retained := lease.OriginalOutcome()
	_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
	if !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
		t.Fatal("absent original read rewrote DROP receipt")
	}
	providerQuery(t, q, f.Database, "CREATE TABLE "+qualified(object)+"(id UInt64 NOT NULL, v Binary)")
	if retry := lease.Operations().Drop(context.Background(), snapshot); retry.State != "not_executed" || !errors.Is(retry.Error, schemaddl.ErrConflict) {
		t.Fatal("old confirmation dropped recreated table", retry.State, retry.Error)
	}
	incarnation, err := s.TableMetadata(context.Background(), object)
	if err != nil || incarnation.Incarnation == snapshot.Schema.Databend.TableID {
		t.Fatal("independent recreation not preserved", err)
	}
	writeDataEvidence(t, f, "schema-drop.json", map[string]any{"original": retained, "dependent_view_retained": true, "dependent_view_query_rejected": true, "recreated_preserved": true, "replacement_table_id": incarnation.Incarnation})
}

func TestDatabendSchemaOriginalDropLostReplyAndConcurrentMove(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	var dispatches atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		target := req.Method == "POST" && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("DROP TABLE"))
		lost := target && bytes.Contains(raw, []byte("drop_lost58"))
		moved := target && bytes.Contains(raw, []byte("drop_moved58"))
		if lost || moved {
			dispatches.Add(1)
		}
		if moved {
			localNativeQuery(t, f.localTransportFixture, "ALTER TABLE "+quoteName(f.Database)+".drop_moved58 RENAME TO drop_saved58")
			localNativeQuery(t, f.localTransportFixture, "CREATE TABLE "+quoteName(f.Database)+".drop_moved58(id UInt64)")
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if lost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 812, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	for _, name := range []string{"drop_lost58", "drop_moved58"} {
		providerQuery(t, q, f.Database, "CREATE TABLE "+name+"(id UInt64)")
		providerQuery(t, q, f.Database, "INSERT INTO "+name+" VALUES(18446744073709551615)")
		lease, snapshot := nativeSchemaDocument(t, s, "lease-"+name, name)
		out := lease.Operations().Drop(context.Background(), snapshot)
		expected := "uncertain"
		if name == "drop_moved58" {
			expected = "committed"
		}
		if out.State != expected || out.Verified || out.Receipt == nil || out.Actual != nil || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) {
			t.Fatal("unverified DROP falsely settled", name, out.State, out.Error)
		}
		if name == "drop_moved58" {
			rows := localNativeQuery(t, f.localTransportFixture, "SELECT to_string(table_id) FROM system.tables WHERE database="+quoteText(f.Database)+" AND name='drop_saved58'")
			data := localNativeQuery(t, f.localTransportFixture, "SELECT id FROM "+quoteName(f.Database)+".drop_saved58")
			if len(rows) != 1 || rows[0][0] != snapshot.Schema.Databend.TableID || len(data) != 1 || data[0][0] != "18446744073709551615" {
				t.Fatal("concurrent original move was not preserved", rows, data)
			}
		}
		rows := localNativeQuery(t, f.localTransportFixture, "SELECT name FROM system.tables WHERE database="+quoteText(f.Database)+" AND name="+quoteText(name))
		if len(rows) != 0 {
			t.Fatal("actual DROP did not remove requested name", rows)
		}
		retained := lease.OriginalOutcome()
		count := dispatches.Load()
		_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
		retry := lease.Operations().Drop(context.Background(), snapshot)
		if retry.State != "not_executed" || !errors.Is(retry.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != count || !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
			t.Fatal("later read/replay changed original DROP evidence", retry)
		}
		writeDataEvidence(t, f, "schema-"+name+".json", map[string]any{"original": retained, "replay_rejected": true, "original_name_absent": true, "dispatches": count, "concurrent_original_preserved": name == "drop_moved58"})
	}
	if dispatches.Load() != 2 {
		t.Fatal("unexpected DDL submission count", dispatches.Load())
	}
}

func TestDatabendSchemaOriginalDropCancelledVerificationAndClose(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 813, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	for _, closing := range []bool{false, true} {
		name := "drop_cancel58"
		if closing {
			name = "drop_close58"
		}
		providerQuery(t, q, f.Database, "CREATE TABLE "+name+"(id UInt64)")
		lease, snapshot := nativeSchemaDocument(t, s, "lease-"+name, name)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		previous := s.observe
		s.observe = func(method string, r agentbridge.Receipt) {
			previous(method, r)
			if method == "schema-drop" {
				close(entered)
				<-release
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan contract.SchemaDesignOutcome, 1)
		go func() { done <- lease.Operations().Drop(ctx, snapshot) }()
		select {
		case <-entered:
		case <-time.After(60 * time.Second):
			t.Fatal("original DROP acknowledgement missing")
		}
		closed := make(chan struct{})
		if closing {
			go func() { _ = lease.Close(); close(closed) }()
		} else {
			cancel()
		}
		select {
		case <-done:
			t.Fatal("unsettled DROP released early")
		case <-closed:
			t.Fatal("close skipped original settlement")
		case <-time.After(50 * time.Millisecond):
		}
		once.Do(func() { close(release) })
		var out contract.SchemaDesignOutcome
		select {
		case out = <-done:
		case <-time.After(60 * time.Second):
			t.Fatal("DROP did not settle")
		}
		cancel()
		s.observe = previous
		if out.State != "committed" || out.Verified || out.Receipt == nil || out.Error == nil {
			t.Fatal("cancel rewrote acknowledged DROP", out.State, out.Error)
		}
		if closing {
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("close did not finish")
			}
			if lease.OriginalOutcome() != nil {
				t.Fatal("closed document retained evidence")
			}
		} else {
			if lease.Operations().CanDrop(snapshot.Target) {
				t.Fatal("unverified DROP unlocked")
			}
			retained := lease.OriginalOutcome()
			_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
			if !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
				t.Fatal("read changed original cancellation evidence")
			}
		}
		writeDataEvidence(t, f, "schema-"+name+".json", map[string]any{"original": out, "settlement_waited": true, "close": closing})
	}
}

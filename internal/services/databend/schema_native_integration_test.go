package databend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func nativeSchemaDocument(t *testing.T, s *Session, id, name string) (contract.SchemaDesignDocumentLease, contract.SchemaDesignSnapshot) {
	t.Helper()
	target := contract.SchemaDesignTarget{Object: s.Namespace("").Object("table", name), Generation: s.Generation()}
	lease, err := s.ReserveSchemaDesignDocument(contract.SchemaDesignDocumentRequest{DocumentID: id, Target: target})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	snapshot, err := lease.Operations().Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal("native schema snapshot", err)
	}
	return lease, snapshot
}

func TestDatabendSchemaOriginalRenameIdentityAndGuards(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 801, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	name, destination := `original_%猫"table`, `renamed_%猫"table`
	object := s.Namespace("").Object("table", name)
	providerQuery(t, q, f.Database, "CREATE TABLE "+qualified(object)+" (id UInt64 NOT NULL, v Binary, txt String DEFAULT 'original')")
	providerQuery(t, q, f.Database, "INSERT INTO "+qualified(object)+" (id,v) VALUES(18446744073709551615,unhex('00ff80'))")
	before := localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(v),txt FROM "+qualified(object))
	lease, snapshot := nativeSchemaDocument(t, s, "original-rename", name)
	sibling, _ := nativeSchemaDocument(t, s, "sibling-rename", name)
	request := contract.SchemaRenameRequest{Original: snapshot, Destination: destination}
	if out := sibling.Operations().Rename(context.Background(), request); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("sibling proof admitted", out)
	}
	forged := request.Clone()
	forged.Original.Schema.DDL += " "
	if out := lease.Operations().Rename(context.Background(), forged); out.State != "not_executed" || out.Error == nil {
		t.Fatal("mutated proof admitted", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := lease.Operations().Rename(ctx, request); out.State != "not_executed" || out.Error == nil {
		t.Fatal("pre-canceled DDL dispatched", out)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE occupied57 (id UInt64)")
	occupied := request.Clone()
	occupied.Destination = "occupied57"
	if out := lease.Operations().Rename(context.Background(), occupied); out.State != "not_executed" || !errors.Is(out.Error, schemaddl.ErrConflict) || out.Receipt != nil {
		t.Fatal("occupied destination dispatched", out)
	}
	out := lease.Operations().Rename(context.Background(), request)
	if out.State != "committed" || !out.Verified || out.Error != nil || out.Actual == nil || out.Receipt == nil || out.Actual.Schema.Databend.TableID != snapshot.Schema.Databend.TableID || out.Receipt.Sequence != 1 {
		t.Fatal("original rename not verified", out.State, out.Error)
	}
	var envelope struct {
		Receipt  agentbridge.Receipt
		Original json.RawMessage
	}
	if json.Unmarshal([]byte(out.Receipt.NativeJSON), &envelope) != nil || !acknowledgedDDL(envelope.Receipt) || !json.Valid(envelope.Original) {
		t.Fatal("original DDL acknowledgement lost")
	}
	object.Name = destination
	after := localNativeQuery(t, f.localTransportFixture, "SELECT id,hex(v),txt FROM "+qualified(object))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rename changed original exact rows", before, after)
	}
	actual, err := lease.Operations().Snapshot(context.Background(), out.Actual.Target)
	if err != nil || !reflect.DeepEqual(actual, *out.Actual) {
		t.Fatal("document did not retain new original identity", err)
	}
	retained := lease.OriginalOutcome()
	out.Receipt.NativeJSON = "caller mutation"
	out.Actual.Schema.Databend.TableID = "999"
	if !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
		t.Fatal("caller changed retained original evidence")
	}
	if retry := lease.Operations().Rename(context.Background(), request); retry.State != "not_executed" || retry.Error == nil {
		t.Fatal("old request replayed")
	}
	providerQuery(t, q, f.Database, "CREATE TABLE recreated57 (id UInt64)")
	recreated, old := nativeSchemaDocument(t, s, "recreated-rename", "recreated57")
	providerQuery(t, q, f.Database, "DROP TABLE recreated57; CREATE TABLE recreated57 (id UInt64)")
	if result := recreated.Operations().Rename(context.Background(), contract.SchemaRenameRequest{Original: old, Destination: "wrong_incarnation57"}); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) {
		t.Fatal("recreated identical table used old confirmation", result)
	}
	providerQuery(t, q, f.Database, "CREATE TABLE altered57 (id UInt64)")
	altered, old := nativeSchemaDocument(t, s, "altered-rename", "altered57")
	providerQuery(t, q, f.Database, "ALTER TABLE altered57 ADD COLUMN added String")
	if result := altered.Operations().Rename(context.Background(), contract.SchemaRenameRequest{Original: old, Destination: "wrong_definition57"}); result.State != "not_executed" || !errors.Is(result.Error, schemaddl.ErrConflict) {
		t.Fatal("changed definition used old confirmation", result)
	}
	writeDataEvidence(t, f, "schema-rename.json", map[string]any{"original": retained, "before": before, "after": after, "sibling_rejected": true, "recreated_rejected": true, "altered_rejected": true, "occupied_rejected": true})
}

func TestDatabendSchemaOriginalLostReplyNeverReplays(t *testing.T) {
	f := managedNativeFixture(t)
	var dispatches atomic.Int32
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
		target := req.Method == "POST" && req.URL.Path == "/v1/query" && bytes.Contains(raw, []byte("ALTER TABLE")) && bytes.Contains(raw, []byte("lost57"))
		if target {
			dispatches.Add(1)
		}
		code, headers, body := forwardFixture(t, f.Endpoint, req)
		if target {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"original_reply_dropped":true}`))
			return
		}
		fixtureResponse(w, code, headers, body)
	}))
	defer product.Close()
	_ = managedNativeStore(t, f)
	proxied := f
	proxied.Endpoint = product.URL
	s := openNativeProvider(t, proxied, nativeProviderProfile(t, proxied, 802, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	providerQuery(t, q, f.Database, "CREATE TABLE lost57(id UInt64)")
	lease, snapshot := nativeSchemaDocument(t, s, "lost-rename", "lost57")
	request := contract.SchemaRenameRequest{Original: snapshot, Destination: "lost_destination57"}
	out := lease.Operations().Rename(context.Background(), request)
	if out.State != "uncertain" || out.Verified || out.Receipt == nil || !errors.Is(out.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != 1 {
		t.Fatal("lost original DDL reply treated as success", out.State, out.Error, dispatches.Load())
	}
	rows := localNativeQuery(t, f.localTransportFixture, "SELECT name,to_string(table_id) FROM system.tables WHERE database="+quoteText(f.Database)+" AND name='lost_destination57'")
	if len(rows) != 1 || rows[0][1] != snapshot.Schema.Databend.TableID {
		t.Fatal("lost reply fixture did not rename original table", rows)
	}
	original := lease.OriginalOutcome()
	_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
	if retry := lease.Operations().Rename(context.Background(), request); retry.State != "not_executed" || !errors.Is(retry.Error, contract.ErrSchemaCommitUncertain) || dispatches.Load() != 1 {
		t.Fatal("unknown write replayed", retry, dispatches.Load())
	}
	if !reflect.DeepEqual(original, lease.OriginalOutcome()) {
		t.Fatal("later read or rejected replay changed original receipt")
	}
	writeDataEvidence(t, f, "schema-lost-reply.json", map[string]any{"original": original, "dispatches": dispatches.Load(), "independent_destination": rows, "read_did_not_settle": true})
}

func TestDatabendSchemaOriginalCancellationAndCloseWaitForSettlement(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 803, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	for _, closing := range []bool{false, true} {
		name := "cancel57"
		if closing {
			name = "close57"
		}
		providerQuery(t, q, f.Database, "CREATE TABLE "+name+" (id UInt64)")
		lease, snapshot := nativeSchemaDocument(t, s, "lease-"+name, name)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		previous := s.observe
		s.observe = func(method string, r agentbridge.Receipt) {
			previous(method, r)
			if method == "schema-rename" {
				close(entered)
				<-release
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan contract.SchemaDesignOutcome, 1)
		go func() {
			result <- lease.Operations().Rename(ctx, contract.SchemaRenameRequest{Original: snapshot, Destination: name + "_renamed"})
		}()
		select {
		case <-entered:
		case <-time.After(60 * time.Second):
			once.Do(func() { close(release) })
			t.Fatal("original acknowledgement did not arrive")
		}
		closed := make(chan struct{})
		if closing {
			go func() { _ = lease.Close(); close(closed) }()
		} else {
			cancel()
		}
		select {
		case <-result:
			t.Fatal("cancellation released unsettled operation")
		case <-closed:
			t.Fatal("close released unsettled operation")
		case <-time.After(50 * time.Millisecond):
		}
		once.Do(func() { close(release) })
		var out contract.SchemaDesignOutcome
		select {
		case out = <-result:
		case <-time.After(60 * time.Second):
			t.Fatal("original operation did not settle")
		}
		cancel()
		s.observe = previous
		if out.State != "committed" || out.Verified || out.Receipt == nil || out.Error == nil || !strings.Contains(out.Receipt.NativeJSON, "ExecutorAcknowledged") {
			t.Fatal("canceled verification erased original acknowledgement", out.State, out.Error)
		}
		if closing {
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("close did not release")
			}
			if lease.OriginalOutcome() != nil {
				t.Fatal("close retained cache")
			}
		} else {
			if lease.Operations().CanRename(snapshot.Target) {
				t.Fatal("unverified acknowledgement allowed replay")
			}
			retained := lease.OriginalOutcome()
			_, _ = lease.Operations().Snapshot(context.Background(), snapshot.Target)
			if !reflect.DeepEqual(retained, lease.OriginalOutcome()) {
				t.Fatal("read rewrote canceled original receipt")
			}
		}
		writeDataEvidence(t, f, "schema-"+name+".json", map[string]any{"original": out, "settlement_waited": true, "close": closing})
	}
}

package databend

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestDatabendSchemaTruncateCancellationAndCloseWaitForSettlement(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 833, filepath.Join(f.Directory, t.Name(), "store")))
	q := reserveNativeQuery(t, s, 1)
	for _, closing := range []bool{false, true} {
		name := "truncate_cancel62"
		if closing {
			name = "truncate_close62"
		}
		providerQuery(t, q, f.Database, "CREATE TABLE "+name+" (id UInt64)")
		providerQuery(t, q, f.Database, "INSERT INTO "+name+" VALUES(1),(2)")
		lease, snapshot := nativeSchemaDocument(t, s, "lease-"+name, name)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		previous := s.observe
		s.observe = func(method string, r agentbridge.Receipt) {
			previous(method, r)
			if method == "schema-truncate" {
				close(entered)
				<-release
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan contract.SchemaDesignOutcome, 1)
		go func() {
			result <- lease.Operations().Truncate(ctx, snapshot.Clone())
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
		if out.State != "committed" || out.Verified || out.Rows != 0 || out.Receipt == nil || out.Error == nil || !strings.Contains(out.Receipt.NativeJSON, "ExecutorAcknowledged") {
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
			if lease.Operations().CanTruncate(snapshot.Target) {
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

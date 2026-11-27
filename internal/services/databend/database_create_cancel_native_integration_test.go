package databend

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

func TestDatabendDatabaseCreateCancellationAndCloseWaitForSettlement(t *testing.T) {
	f := managedNativeFixture(t)
	_ = managedNativeStore(t, f)
	s := openNativeProvider(t, f, nativeProviderProfile(t, f, 903, filepath.Join(f.Directory, t.Name(), "store")))
	for _, closing := range []bool{false, true} {
		name := "create_cancel61"
		if closing {
			name = "create_close61"
		}
		d := nativeDatabaseCreateDocument(t, s, "lease-"+name, name)
		before, err := d.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		previous := s.observe
		s.observe = func(method string, r agentbridge.Receipt) {
			previous(method, r)
			if method == "database-create" {
				close(entered)
				<-release
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan contract.DatabaseCreateOutcome, 1)
		go func() { result <- d.Create(ctx, before) }()
		select {
		case <-entered:
		case <-time.After(60 * time.Second):
			t.Fatal("original create acknowledgement missing")
		}
		closed := make(chan struct{})
		if closing {
			go func() { _ = d.Close(); close(closed) }()
		} else {
			cancel()
		}
		select {
		case <-result:
			t.Fatal("cancellation skipped original settlement")
		case <-closed:
			t.Fatal("close skipped original settlement")
		case <-time.After(50 * time.Millisecond):
		}
		once.Do(func() { close(release) })
		var out contract.DatabaseCreateOutcome
		select {
		case out = <-result:
		case <-time.After(60 * time.Second):
			t.Fatal("original create did not settle")
		}
		cancel()
		s.observe = previous
		if out.State != "committed" || out.Verified || out.Receipt == nil || out.Error == nil {
			t.Fatal("original ACK lost", out)
		}
		rows := localNativeQuery(t, f.localTransportFixture, "SELECT name FROM system.databases WHERE name="+quoteText(name)+" AND dropped_on IS NULL")
		if !reflect.DeepEqual(rows, [][]string{{name}}) {
			t.Fatal("cancel treated as rollback", rows)
		}
		if closing {
			select {
			case <-closed:
			case <-time.After(10 * time.Second):
				t.Fatal("close did not finish")
			}
			if d.OriginalOutcome() != nil {
				t.Fatal("closed receipt retained")
			}
		} else {
			original := d.OriginalOutcome()
			_, _ = d.Snapshot(context.Background())
			if retry := d.Create(context.Background(), before); retry.State != "not_executed" || retry.Receipt != nil {
				t.Fatal("canceled verification replayed", retry)
			}
			if !reflect.DeepEqual(original, d.OriginalOutcome()) {
				t.Fatal("later read changed original")
			}
		}
		writeDataEvidence(t, f, "database-"+name+".json", map[string]any{"original": out, "settlement_waited": true, "close": closing, "independent_rows": rows})
	}
}

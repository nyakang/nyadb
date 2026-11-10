package dataworkspace

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func keyReadWorkspace(t *testing.T) (*State, *Document, *Session, contract.KVKeyRead) {
	t.Helper()
	s, d, session := mutationWorkspace(t)
	d.page.Object = d.object
	d.page.CurrentCursor = &contract.DataCursor{Values: [][]byte{[]byte("original signed cursor")}}
	key := bytes.Repeat([]byte{0, 255}, 2048)
	d.page.RowSnapshots[0].Ref.Values = [][]byte{key}
	d.page.Rows[0][0].Text = "truncated display key"
	r := contract.KVKeyRead{Scope: contract.KVScope{Namespace: d.object.Namespace(), Generation: d.generation}, Key: key, Revision: 9007199254740993}
	s.deps.OpenKVEvents = func(ui.Services, contract.KVKeyRead, bool) bool { return true }
	d.boundRuntime.CaptureKVKeyRead = func(context.Context, int64, string, string, contract.KVKeyReadRequest) (*contract.KVKeyRead, error) {
		return &r, nil
	}
	return s, d, session, r
}

func waitKeyRead(t *testing.T, s *State, d *Document) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for d.keyRead != nil && time.Now().Before(deadline) {
		s.Consume(ui.Services{})
		time.Sleep(time.Millisecond)
	}
	if d.keyRead != nil {
		t.Fatal("original key read did not settle")
	}
}

func TestDataKeyEventsOwnCompleteOriginalInputAndParkUntilDocumentActivation(t *testing.T) {
	s, d, _, original := keyReadWorkspace(t)
	active := true
	s.deps.KVReadActive = func(*Document) bool { return active }
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	d.boundRuntime.CaptureKVKeyRead = func(_ context.Context, id int64, db, table string, r contract.KVKeyReadRequest) (*contract.KVKeyRead, error) {
		close(entered)
		<-release
		if id != original.Scope.Namespace.ProfileID || db != d.database || table != d.table || !bytes.Equal(r.Snapshot.Ref.Values[0], original.Key) || string(r.Cursor.Values[0]) != "original signed cursor" {
			t.Error("worker substituted displayed key or mutable input")
		}
		return &original, nil
	}
	opened := 0
	s.deps.OpenKVEvents = func(_ ui.Services, r contract.KVKeyRead, history bool) bool {
		opened++
		if !history || !bytes.Equal(r.Key, original.Key) || r.Revision != original.Revision {
			t.Error("host received different original key/revision")
		}
		return true
	}
	s.OpenKeyEvents(ui.Services{}, d, 0, true)
	<-entered
	op := d.keyRead
	active = false
	d.page.RowSnapshots[0].Ref.Values[0] = []byte("later input")
	d.page.CurrentCursor.Values[0] = []byte("later cursor")
	once.Do(func() { close(release) })
	<-op.finished
	s.Consume(ui.Services{})
	if opened != 0 || d.keyRead != op {
		t.Fatal("parked completion changed another document")
	}
	active = true
	waitKeyRead(t, s, d)
	if opened != 1 || d.keyReadError != "" {
		t.Fatal("original completion was lost", d.keyReadError)
	}
}

func TestDataKeyEventsCancelWaitsAndGenerationOrPageChangeRejectsLateResult(t *testing.T) {
	for _, mode := range []string{"cancel", "generation", "page", "close"} {
		t.Run(mode, func(t *testing.T) {
			s, d, session, r := keyReadWorkspace(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			d.boundRuntime.CaptureKVKeyRead = func(ctx context.Context, _ int64, _, _ string, _ contract.KVKeyReadRequest) (*contract.KVKeyRead, error) {
				close(entered)
				<-release
				return &r, ctx.Err()
			}
			opened := 0
			s.deps.OpenKVEvents = func(ui.Services, contract.KVKeyRead, bool) bool { opened++; return true }
			s.OpenKeyEvents(ui.Services{}, d, 0, false)
			<-entered
			if mode == "cancel" || mode == "close" {
				s.cancelKVKeyRead(d)
			}
			if mode == "generation" {
				session.Generation++
			}
			if mode == "page" {
				copy := *d.page
				d.page = &copy
			}
			s.Consume(ui.Services{})
			if d.keyRead == nil {
				t.Fatal("cancellation forgot original executor")
			}
			once.Do(func() { close(release) })
			if mode == "close" {
				s.Close(d)
			} else {
				waitKeyRead(t, s, d)
			}
			if opened != 0 || d.keyRead != nil {
				t.Fatal("late key result opened replacement document")
			}
		})
	}
}

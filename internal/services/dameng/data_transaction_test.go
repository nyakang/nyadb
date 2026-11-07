package dameng

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type acknowledgedDataTx struct {
	entered, acknowledge chan struct{}
	once                 sync.Once
}

func (*acknowledgedDataTx) Commit() error                         { return contract.ErrUnsupported }
func (t *acknowledgedDataTx) CommitContext(context.Context) error { return contract.ErrUnsupported }
func (t *acknowledgedDataTx) Rollback() error {
	t.once.Do(func() { close(t.entered) })
	<-t.acknowledge
	return nil
}
func (t *acknowledgedDataTx) RollbackContext(context.Context) error { return t.Rollback() }

func TestDataTransactionCloseAndSessionCloseWaitForReadAndRollbackAcknowledgement(t *testing.T) {
	c := &waitingConnector{entered: make(chan struct{}), canceled: make(chan struct{}), acknowledge: make(chan struct{})}
	raw := &acknowledgedDataTx{entered: make(chan struct{}), acknowledge: make(chan struct{})}
	var readOnce, rollbackOnce sync.Once
	releaseRead := func() { readOnce.Do(func() { close(c.acknowledge) }) }
	releaseRollback := func() { rollbackOnce.Do(func() { close(raw.acknowledge) }) }
	defer releaseRead()
	defer releaseRollback()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{profile: connections.Profile{ID: 1}, ctx: ctx, cancel: cancel, db: sql.OpenDB(c), sqlDB: sql.OpenDB(c), dataDB: sql.OpenDB(c), closeDone: make(chan struct{}), dataTransactions: make(map[*dataTransaction]struct{})}
	conn, err := s.dataDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lifetime, stop := context.WithCancel(ctx)
	token := &dataTransaction{s: s, ctx: lifetime, cancel: stop, closeDone: make(chan struct{}), conn: conn, tx: raw}
	s.dataTransactions[token] = struct{}{}
	defer func() { releaseRead(); releaseRollback(); s.Close() }()
	read := make(chan error, 1)
	go func() {
		token.ops.Lock()
		defer token.ops.Unlock()
		current, done, e := token.request(context.Background())
		if e != nil {
			read <- e
			return
		}
		defer done()
		rows, e := token.conn.QueryContext(current, "captured read")
		if e != nil {
			read <- e
			return
		}
		for rows.Next() {
		}
		read <- errors.Join(rows.Err(), rows.Close())
	}()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("original read did not enter")
	}
	closed, sessionClosed := make(chan struct{}), make(chan error, 1)
	go func() { token.Close(); close(closed) }()
	<-c.canceled
	go func() { sessionClosed <- s.Close() }()
	select {
	case <-closed:
		t.Fatal("token skipped read acknowledgement")
	case <-sessionClosed:
		t.Fatal("session skipped read acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	releaseRead()
	select {
	case <-raw.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("original rollback did not enter")
	}
	select {
	case <-closed:
		t.Fatal("token skipped rollback acknowledgement")
	case <-sessionClosed:
		t.Fatal("session skipped rollback acknowledgement")
	case <-time.After(30 * time.Millisecond):
	}
	releaseRollback()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("token didn't complete")
	}
	select {
	case err := <-sessionClosed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session didn't complete")
	}
	if err := <-read; !errors.Is(err, context.Canceled) {
		t.Fatal("original read cancellation lost", err)
	}
}

func TestDataTransactionMergesOriginalEvidenceAcrossChangedKeysAndInsertDelete(t *testing.T) {
	s := &Session{dataScope: "owned"}
	table := &contract.TableStructure{Object: contract.ObjectRef{ProfileID: 1, Catalog: "DAMENG", Schema: "S", Kind: "table", Name: "T"}, Columns: []contract.StructureColumn{{Name: "ID", ValueKind: contract.Integer}, {Name: "B", ValueKind: contract.Binary}}}
	snapshot := func(id string, b byte) *contract.RowSnapshot {
		return s.fullSnapshot(table, []string{"ID"}, []contract.Cell{{Kind: contract.Integer, Text: id}, {Kind: contract.Binary, Bytes: []byte{b}}}, "table")
	}
	old, middle, last := snapshot("1", 1), snapshot("2", 2), snapshot("3", 3)
	previous := []contract.DataEditOutcome{{Kind: "update", Original: old, Saved: middle}}
	batch := []contract.DataEditOutcome{{Kind: "update", Original: middle, Saved: last}}
	merged, err := mergeDataTransactionOutcomes(previous, batch)
	if err != nil || len(merged) != 1 || merged[0].Original.Ref.TypedValues[0].Text != "1" || merged[0].Saved.Ref.TypedValues[0].Text != "3" {
		t.Fatal("original evidence lost across changed key", err)
	}
	last.Original[1].Bytes[0] = 255
	if merged[0].Saved.Original[1].Bytes[0] != 3 {
		t.Fatal("pending evidence aliases result")
	}
	deleted, err := mergeDataTransactionOutcomes(merged, []contract.DataEditOutcome{{Kind: "delete", Original: merged[0].Saved}})
	if err != nil || len(deleted) != 1 || deleted[0].Kind != "delete" || deleted[0].Original.Ref.TypedValues[0].Text != "1" || deleted[0].Saved != nil {
		t.Fatal("delete lost original identity", err)
	}
	none, err := mergeDataTransactionOutcomes([]contract.DataEditOutcome{{Kind: "insert", Saved: middle}}, []contract.DataEditOutcome{{Kind: "delete", Original: middle}})
	if err != nil || len(none) != 0 {
		t.Fatal("insert/delete did not settle to no net row", err)
	}
}

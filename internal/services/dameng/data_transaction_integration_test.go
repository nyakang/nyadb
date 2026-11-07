package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestNativeDamengDataTransactionSavepointSyntaxAndCurrentCommitContext(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_TX_SYNTAX_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE "+q+"(ID INT PRIMARY KEY,V INT)"); err != nil {
		t.Fatal(err)
	}
	defer s.db.ExecContext(context.Background(), "DROP TABLE "+q)
	c, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	beginCtx, stopBegin := context.WithCancel(ctx)
	var tx dpi.ContextTransaction
	err = c.Raw(func(raw any) error {
		t, e := raw.(driver.ConnBeginTx).BeginTx(beginCtx, driver.TxOptions{})
		if e == nil {
			tx = t.(dpi.ContextTransaction)
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stopBegin()
	for _, text := range []string{
		"INSERT INTO " + q + " VALUES(1,1)",
		"SAVEPOINT NYADB_DATA_BATCH",
		"UPDATE " + q + " SET V=2 WHERE ID=1",
		"ROLLBACK TO SAVEPOINT NYADB_DATA_BATCH",
		"SAVEPOINT NYADB_DATA_BATCH",
		"INSERT INTO " + q + " VALUES(2,3)",
	} {
		if _, err = c.ExecContext(ctx, text); err != nil {
			t.Fatal(text, err)
		}
	}
	var v int
	if err = c.QueryRowContext(ctx, "SELECT V FROM "+q+" WHERE ID=1").Scan(&v); err != nil || v != 1 {
		t.Fatal("savepoint lost earlier batch", err, v)
	}
	if _, err = c.ExecContext(ctx, "RELEASE SAVEPOINT NYADB_DATA_BATCH"); err != nil {
		t.Log("native release syntax rejected", err)
	} else {
		t.Log("native release syntax accepted")
	}
	if err = tx.CommitContext(ctx); err != nil {
		t.Fatal("commit retained expired begin scope", err)
	}
	if err = c.QueryRowContext(ctx, "SELECT SUM(V) FROM "+q).Scan(&v); err != nil || v != 4 {
		t.Fatal("native commit", err, v)
	}
}

func TestNativeDamengDataTransactionCancellationRetainsUncertaintyAndNeverReplays(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	admin := nativeDB(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 45*time.Second)
	defer stop()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_TX_CANCEL_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+"(ID INT PRIMARY KEY,V VARCHAR(32))"); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP TABLE "+q)
	if _, err := admin.ExecContext(ctx, "INSERT INTO "+q+" VALUES(1,'original')"); err != nil {
		t.Fatal(err)
	}
	trigger := quote(o.Schema) + "." + quote(o.Name+"_TR")
	if _, err := admin.ExecContext(ctx, "CREATE TRIGGER "+trigger+" BEFORE UPDATE ON "+q+" FOR EACH ROW BEGIN SYS.DBMS_LOCK.SLEEP(5); END;"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := admin.QueryRowContext(ctx, "SELECT STATUS FROM SYS.ALL_OBJECTS WHERE OWNER=? AND OBJECT_NAME=? AND OBJECT_TYPE='TRIGGER'", o.Schema, o.Name+"_TR").Scan(&status); err != nil || status != "VALID" {
		t.Fatal("sleep trigger must be VALID", err, status)
	}
	owner := observeDataTransactions(t, s)
	r := s.DataTransactions()
	token, err := r.Begin(ctx, s.ProfileID(), o.Schema, o.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	page, err := r.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	owner.beforeExec = func(q string) {
		if strings.HasPrefix(q, "BEGIN UPDATE ") {
			close(entered)
		}
	}
	write, cancel := context.WithCancel(ctx)
	defer cancel()
	type completion struct {
		result *contract.DataEditResult
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, e := r.Save(write, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"V": {Text: "must not replay"}}}})
		done <- completion{result, e}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native transaction write didn't enter", ctx.Err())
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	var result completion
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("native cancellation didn't acknowledge", ctx.Err())
	}
	if !errors.Is(result.err, contract.ErrDataTransactionLost) || !errors.Is(result.err, contract.ErrDataCommitUncertain) || result.result == nil || !result.result.CommitUncertain || !result.result.Incomplete || result.result.CommitConfirmed {
		t.Fatal("canceled native write lost uncertainty", result.err, result.result)
	}
	if _, err = r.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"V": {Text: "replay"}}}}); !errors.Is(err, contract.ErrDataTransactionLost) {
		t.Fatal("canceled token reconnected", err)
	}
	if owner.mutations.Load() != 1 || owner.commits.Load() != 0 {
		t.Fatal("canceled write replayed")
	}
	// Native procedural sleep can retain the lock after client acknowledgement.
	// Only independent bounded reads poll its eventual rollback; no UPDATE retry.
	started := time.Now()
	var got string
	for {
		err = admin.QueryRowContext(ctx, "SELECT V FROM "+q+" WHERE ID=1").Scan(&got)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "code -608") || time.Since(started) > 12*time.Second {
			t.Fatal("independent cancellation read", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != "original" {
		t.Fatal("canceled transaction persisted", got)
	}
}

func TestNativeDamengDataTransactionCloseWaitsForCapturedSaveAcknowledgement(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	admin := nativeDB(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_TX_ACK_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+"(ID INT PRIMARY KEY,V VARCHAR(32))"); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP TABLE "+q)
	if _, err := admin.ExecContext(ctx, "INSERT INTO "+q+" VALUES(1,'original')"); err != nil {
		t.Fatal(err)
	}
	owner := observeDataTransactions(t, s)
	r := s.DataTransactions()
	token, err := r.Begin(ctx, s.ProfileID(), o.Schema, o.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	page, err := r.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseAck := func() { once.Do(func() { close(release) }) }
	defer releaseAck()
	owner.afterExec = func(q string, e error) {
		if strings.HasPrefix(q, "BEGIN UPDATE ") && e == nil {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		_, e := r.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"V": {Text: "pending"}}}})
		done <- e
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("captured write didn't acknowledge", ctx.Err())
	}
	closed := make(chan struct{})
	go func() { token.Close(); close(closed) }()
	select {
	case e := <-done:
		t.Fatal("save skipped acknowledgement", e)
	case <-closed:
		t.Fatal("close skipped acknowledgement")
	case <-time.After(40 * time.Millisecond):
	}
	releaseAck()
	select {
	case e := <-done:
		if !errors.Is(e, contract.ErrDataTransactionLost) {
			t.Fatal("close didn't revoke original save", e)
		}
	case <-ctx.Done():
		t.Fatal("save didn't settle")
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("close didn't settle")
	}
	var got string
	if err = admin.QueryRowContext(ctx, "SELECT V FROM "+q+" WHERE ID=1").Scan(&got); err != nil || got != "original" {
		t.Fatal("acknowledged close didn't roll back", err, got)
	}
	if owner.mutations.Load() != 1 || owner.commits.Load() != 0 {
		t.Fatal("close replayed original write")
	}
}

func TestNativeDamengDataTransactionQualifiedPendingReadsFailedBatchAndRollback(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, stop := context.WithTimeout(context.Background(), 45*time.Second)
	defer stop()
	r := contract.ComposeData(s)
	begin, stopBegin := context.WithCancel(ctx)
	token, err := r.Transactions.Begin(begin, s.ProfileID(), o.Schema, o.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	stopBegin()
	page, err := r.Transactions.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 2, UseKeyset: true})
	if err != nil || page.NextCursor == nil {
		t.Fatal("qualified transaction page", err)
	}
	row, err := r.Transactions.Row(ctx, token, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.Transactions.Row(ctx, token, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[1])
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("中文🙂", 1800)
	bin := bytes.Repeat([]byte{0, 128, 255}, 1800)
	result, err := r.Transactions.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{
		"T": {Text: "pending"}, "C": {Text: text}, "L": {Binary: true, Bytes: bin}, "N": {Text: "-123456789012345678.12345678901234567891"},
	}}})
	if err != nil || result.Applied != 1 || result.CommitConfirmed || result.CommitUncertain {
		t.Fatal("pending exact save", err, result)
	}
	read, err := r.Transactions.Row(ctx, token, s.ProfileID(), o.Schema, o.Name, result.Outcomes[0].Saved.Ref)
	if err != nil || read.Values["C"].Text != text || !bytes.Equal(read.Values["L"].Bytes, bin) || read.Values["N"].Text != "-123456789012345678.12345678901234567891" {
		t.Fatal("original physical pending row", err)
	}
	chunk, err := r.Transactions.Chunk(ctx, token, s.ProfileID(), o.Schema, o.Name, "C", result.Outcomes[0].Saved.Ref, 3, 1024)
	if err != nil || chunk.Text != string([]rune(text)[3:1027]) || !chunk.HasMore {
		t.Fatal("pending chunk", err)
	}
	page, err = r.Transactions.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 2})
	if err != nil || page.Rows[0][3].Text != "pending" || !page.Rows[0][5].Truncated {
		t.Fatal("pending bounded page", err)
	}
	failed, err := r.Transactions.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{
		{Kind: "update", Original: read.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "failed"}, "C": {Text: "failed LOB"}, "L": {Binary: true, Bytes: []byte{1}}}},
		{Kind: "update", Original: other.Snapshot, Values: map[string]contract.DataValue{"ID": {Text: row.Values["ID"].Text}}},
	})
	if err == nil || failed.Applied != 0 || failed.CommitUncertain {
		t.Fatal("failed batch published", err, failed)
	}
	read, err = r.Transactions.Row(ctx, token, s.ProfileID(), o.Schema, o.Name, read.Snapshot.Ref)
	if err != nil || read.Values["T"].Text != "pending" || read.Values["C"].Text != text || !bytes.Equal(read.Values["L"].Bytes, bin) {
		t.Fatal("savepoint discarded earlier batch", err)
	}
	// MaxConnections=1: held document transaction must not occupy metadata.
	if _, err = s.Namespaces(ctx); err != nil {
		t.Fatal("metadata pool starved", err)
	}
	if err = r.Transactions.Rollback(ctx, token); err != nil {
		t.Fatal(err)
	}
	var got string
	if err = admin.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&got); err != nil || got != row.Values["T"].Text {
		t.Fatal("rollback visible externally", err, got)
	}
	if _, err = r.Transactions.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}); !errors.Is(err, contract.ErrDataTransactionLost) {
		t.Fatal("ended transaction reconnected", err)
	}
}

type transactionReceiptConnector struct{ *editReceiptConnector }
type transactionReceiptConn struct{ *editReceiptConn }
type transactionReceiptTx struct {
	dpi.ContextTransaction
	owner *editReceiptConnector
}

func (c *transactionReceiptConnector) Connect(ctx context.Context) (driver.Conn, error) {
	x, err := c.editReceiptConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &transactionReceiptConn{x.(*editReceiptConn)}, nil
}
func (c *transactionReceiptConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	x, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &transactionReceiptTx{ContextTransaction: x.(dpi.ContextTransaction), owner: c.owner}, nil
}
func (t *transactionReceiptTx) CommitContext(ctx context.Context) error {
	t.owner.commits.Add(1)
	err := t.ContextTransaction.CommitContext(ctx)
	if err == nil && t.owner.armed.CompareAndSwap(true, false) {
		return errors.Join(dpi.ErrOutcomeUnknown, dpi.ErrSessionClosed)
	}
	return err
}
func observeDataTransactions(t *testing.T, s *Session) *editReceiptConnector {
	t.Helper()
	cfg, err := connections.DamengProfileConfig(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	_, password := nativeFixture(t)
	c, err := NewSessionConnector(cfg, password)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dataDB.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &editReceiptConnector{Connector: c}
	s.dataDB = sql.OpenDB(&transactionReceiptConnector{owner})
	s.dataDB.SetMaxOpenConns(1)
	return owner
}

func TestNativeDamengDataTransactionCommitAndLostReceiptRetainNetEvidenceWithoutReplay(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "lost"}[lost], func(t *testing.T) {
			cfg, password := nativeFixture(t)
			s := fixtureSession(t, cfg, password)
			admin := nativeDB(t, cfg, password)
			ctx, stop := context.WithTimeout(context.Background(), 45*time.Second)
			defer stop()
			o := s.Namespace("SYSDBA").Object("table", "NYADB_TX_COMMIT_"+nativeSuffix(t))
			q := qualified(o)
			if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+"(ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(32) DEFAULT 'default',N DECIMAL(38,20),G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL)"); err != nil {
				t.Fatal(err)
			}
			defer admin.ExecContext(context.Background(), "DROP TABLE "+q)
			if _, err := admin.ExecContext(ctx, "CREATE TRIGGER "+quote(o.Schema)+"."+quote(o.Name+"_TR")+" BEFORE INSERT OR UPDATE ON "+q+" FOR EACH ROW BEGIN :NEW.V := UPPER(:NEW.V); END;"); err != nil {
				t.Fatal(err)
			}
			owner := observeDataTransactions(t, s)
			r := s.DataTransactions()
			begin, stopBegin := context.WithCancel(ctx)
			token, err := r.Begin(begin, s.ProfileID(), o.Schema, o.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer token.Close()
			stopBegin()
			insert, err := r.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "insert"}})
			if err != nil || insert.Outcomes[0].GeneratedID != 9007199254740993 {
				t.Fatal("actual native identity", err)
			}
			update, err := r.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: insert.Outcomes[0].Saved, Values: map[string]contract.DataValue{"V": {Text: "final"}, "N": {Text: "123456789012345678.12345678901234567891"}}}})
			if err != nil {
				t.Fatal(err)
			}
			// Caller results must not own mutable token evidence.
			insert.Outcomes[0].Saved.Original[1].Text = "corrupted caller result"
			update.Outcomes[0].Saved.Original[1].Text = "corrupted caller result"
			owner.armed.Store(lost)
			err = r.Commit(ctx, token)
			if lost {
				var receipt *contract.CommitError
				if !errors.As(err, &receipt) || !errors.Is(err, contract.ErrDataCommitUncertain) || receipt.Result.CommitConfirmed || !receipt.Result.Uncertain || receipt.Result.Incomplete || len(receipt.Result.Saved) != 1 || receipt.Result.Saved[0].Kind != "insert" || receipt.Result.Saved[0].Values["V"].Text != "FINAL" || receipt.Result.Saved[0].Values["N"].Text != "123456789012345678.12345678901234567891" {
					t.Fatal("net complete commit evidence", err)
				}
			} else if err != nil {
				t.Fatal("confirmed original commit", err)
			}
			if owner.commits.Load() != 1 || owner.mutations.Load() != 2 {
				t.Fatal("commit replayed write")
			}
			if err = r.Commit(ctx, token); !errors.Is(err, contract.ErrDataTransactionLost) {
				t.Fatal("commit token reused", err)
			}
			var v, n string
			var id int64
			if err = admin.QueryRowContext(ctx, "SELECT ID,V,N FROM "+q).Scan(&id, &v, &n); err != nil || id != 9007199254740993 || v != "FINAL" || n != "123456789012345678.12345678901234567891" {
				t.Fatal("independent committed state", err)
			}
			if owner.commits.Load() != 1 || owner.mutations.Load() != 2 {
				t.Fatal("lost commit retried")
			}
		})
	}
}

func TestNativeDamengDataTransactionBindingPoolLimitAndOriginalClose(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, stop := context.WithTimeout(context.Background(), 45*time.Second)
	defer stop()
	r := s.DataTransactions()
	token, err := r.Begin(ctx, s.ProfileID(), o.Schema, o.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	page, err := r.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.Row(ctx, token, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	foreign := &Session{}
	if err = foreign.commitDataTransaction(ctx, token); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign original session committed token", err)
	}
	if err = foreign.rollbackDataTransaction(ctx, token); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("foreign original session rolled back token", err)
	}
	for _, changed := range []struct {
		id           int64
		schema, name string
	}{{s.ProfileID() + 1, o.Schema, o.Name}, {s.ProfileID(), o.Schema, "Other"}, {s.ProfileID(), "SYSDBA", o.Name}} {
		if _, err = r.Page(ctx, token, changed.id, changed.schema, changed.name, contract.DataOptions{}); !errors.Is(err, connections.ErrInvalid) {
			t.Fatal("qualified token changed object", err)
		}
	}
	wrong := o
	wrong.Schema = "SYSDBA"
	if _, err = r.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Object: wrong}); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("explicit object mismatch", err)
	}
	ref := *page.RowRefs[0]
	ref.Object = wrong
	if _, err = r.Row(ctx, token, s.ProfileID(), o.Schema, o.Name, ref); !errors.Is(err, connections.ErrInvalid) {
		t.Fatal("qualified ref mismatch", err)
	}
	short, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	second, err := r.Begin(short, s.ProfileID(), o.Schema, o.Name)
	cancel()
	if err == nil || second != nil || s.dataDB.Stats().OpenConnections != 1 {
		t.Fatal("unbounded data transaction pool", err)
	}
	if _, err = s.Namespaces(ctx); err != nil {
		t.Fatal("metadata starved", err)
	}
	if _, err = r.Save(ctx, token, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "pending close"}}}}); err != nil {
		t.Fatal(err)
	}
	unsubmitted, cancelCommit := context.WithCancel(ctx)
	cancelCommit()
	if err = r.Commit(unsubmitted, token); !errors.Is(err, context.Canceled) || errors.Is(err, contract.ErrDataCommitUncertain) {
		t.Fatal("pre-canceled commit was submitted", err)
	}
	if _, err = r.Page(ctx, token, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1}); err != nil {
		t.Fatal("unsubmitted commit lost rollback opportunity", err)
	}
	var group sync.WaitGroup
	for range 2 {
		group.Go(token.Close)
	}
	group.Wait()
	var got string
	if err = admin.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&got); err != nil || got != "中文 ' literal %_" {
		t.Fatal("close didn't roll back original", err, got)
	}
	newToken, err := r.Begin(ctx, s.ProfileID(), o.Schema, o.Name)
	if err != nil {
		t.Fatal("pool slot not released", err)
	}
	defer newToken.Close()
	newRow, err := r.Row(ctx, newToken, s.ProfileID(), o.Schema, o.Name, row.Snapshot.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Save(ctx, newToken, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: newRow.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "pending disconnect"}}}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&got); err != nil || got != "中文 ' literal %_" {
		t.Fatal("session close didn't roll back original", err)
	}
	if _, err = r.Page(ctx, newToken, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}); !errors.Is(err, contract.ErrDataTransactionLost) {
		t.Fatal("disconnected token reconnected", err)
	}
	// The native locator rollback defect applies to a held transaction too.
	// Refuse any LOB table with any trigger before publishing a token.
	trigger := quote(o.Schema) + "." + quote("NYADB_TX_LOB_"+nativeSuffix(t))
	if _, err = admin.ExecContext(ctx, "CREATE TRIGGER "+trigger+" BEFORE UPDATE ON "+qualified(o)+" FOR EACH ROW BEGIN NULL; END;"); err != nil {
		t.Fatal(err)
	}
	cfg, password := nativeFixture(t)
	fresh := fixtureSession(t, cfg, password)
	for _, disable := range []bool{false, true} {
		if disable {
			if _, err = admin.ExecContext(ctx, "ALTER TRIGGER "+trigger+" DISABLE"); err != nil {
				t.Fatal(err)
			}
		}
		refused, e := fresh.DataTransactions().Begin(ctx, fresh.ProfileID(), o.Schema, o.Name)
		if !errors.Is(e, contract.ErrDataReadOnly) || refused != nil || fresh.dataDB.Stats().InUse != 0 {
			t.Fatal("unsafe LOB trigger transaction published", e)
		}
	}
}

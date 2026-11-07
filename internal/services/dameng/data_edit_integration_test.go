package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestNativeDamengDataSaveCompleteValuesAtomicBatchAndReadOnlyVerification(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := s.DataReads()
	page, err := r.Loader(ctx, s.ProfileID(), o.Schema, o.Name, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]contract.DataValue{
		"N": {Kind: contract.Decimal, Text: "-123456789012345678.12345678901234567891"},
		"T": {Kind: contract.Text, Text: "saved 中文🙂 ' ?"},
		"B": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 128, 255}},
		"C": {Kind: contract.Text, Text: strings.Repeat("中文🙂", 1800)},
		"L": {Kind: contract.Binary, Binary: true, Bytes: bytes.Repeat([]byte{0, 1, 255}, 1800)},
		"Z": {Kind: contract.Null, Null: true},
		"D": {Kind: contract.DateTime, Text: "2026-11-07 10:20:30.123456"},
	}
	saved, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: values}})
	if err != nil || saved == nil || !saved.CommitConfirmed || saved.CommitUncertain || saved.Incomplete || saved.Applied != 1 || len(saved.Outcomes) != 1 || len(saved.Saved) != 1 || saved.Outcomes[0].Saved == nil {
		t.Fatal("complete native save", err, saved)
	}
	again, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, saved.Outcomes[0].Saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range values {
		if !reflect.DeepEqual(again.Values[name], want) {
			t.Fatal("saved typed field changed", name)
		}
	}
	if saved.Outcomes[0].Previews[5].Value == nil || !saved.Outcomes[0].Previews[5].Truncated || len(saved.Outcomes[0].Previews[5].Text) > 4096 {
		t.Fatal("saved preview materialized the full LOB")
	}
	verified, err := s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, saved)
	if err != nil || verified.Status != "applied" || verified.Recovered == nil || !verified.Recovered.Verified || verified.Recovered.CommitConfirmed {
		t.Fatal("read-only verification claimed a commit receipt", err, verified)
	}
	_, err = s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "stale"}}}})
	if !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("stale complete snapshot accepted", err)
	}
	// An earlier valid write is rolled back when a later UNIQUE violation fails.
	other, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[1])
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{
		{Kind: "update", Original: again.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "must rollback"}, "C": {Text: "rollback CLOB"}, "L": {Binary: true, Bytes: []byte{128, 0, 255}}}},
		{Kind: "update", Original: other.Snapshot, Values: map[string]contract.DataValue{"ID": {Kind: contract.Integer, Text: again.Values["ID"].Text}}},
	})
	if err == nil || result == nil || result.CommitConfirmed || result.CommitUncertain || result.Applied != 0 || len(result.Outcomes) != 0 {
		t.Fatal("failed native batch retained staged results", err, result)
	}
	after, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, again.Snapshot.Ref)
	if err != nil || after.Values["T"].Text != values["T"].Text || after.Values["C"].Text != values["C"].Text || !bytes.Equal(after.Values["L"].Bytes, values["L"].Bytes) {
		t.Fatal("earlier write survived batch rollback", err)
	}
	insert, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{
		"ID": {Kind: contract.Integer, Text: "9007199254741999"}, "GRP": {Kind: contract.Integer, Text: "4"}, "T": {Text: "insert"}, "C": {Text: ""}, "L": {Kind: contract.Binary, Binary: true, Bytes: []byte{}},
	}}})
	if err != nil || !insert.CommitConfirmed || insert.Outcomes[0].Saved.Ref.TypedValues[0].Text != "9007199254741999" {
		t.Fatal("insert actual key", err, insert)
	}
	created, err := r.RowReader(ctx, s.ProfileID(), o.Schema, o.Name, insert.Outcomes[0].Saved.Ref)
	if err != nil || created.Values["C"].Null || created.Values["L"].Null || !created.Values["Z"].Null {
		t.Fatal("insert null/empty values", err)
	}
	deleted, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "delete", Original: created.Snapshot}})
	if err != nil || !deleted.CommitConfirmed || deleted.Outcomes[0].Saved != nil {
		t.Fatal("delete", err, deleted)
	}
	verified, err = s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, deleted)
	if err != nil || verified.Status != "applied" {
		t.Fatal("deleted row verification", err, verified)
	}
	var count int
	if err = admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 4 {
		t.Fatal("native atomic row count", err, count)
	}
}

func TestNativeDamengDataSaveReturnsActualDefaultIdentityGeneratedAndTriggerValues(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_EDIT_DEFAULT_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := s.db.ExecContext(ctx, "CREATE TABLE "+q+"(ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(32) DEFAULT 'default',N DECIMAL(38,20) DEFAULT 1.23456789012345678901,G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.db.ExecContext(context.Background(), "DROP TABLE "+q); err != nil {
			t.Error(err)
		}
	}()
	trigger := quote(o.Schema) + "." + quote(o.Name+"_TR")
	if _, err := s.db.ExecContext(ctx, "CREATE TRIGGER "+trigger+" BEFORE INSERT OR UPDATE ON "+q+" FOR EACH ROW BEGIN :NEW.V := UPPER(:NEW.V); END;"); err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]contract.DataValue{nil, {"V": {Default: true}}, {"V": {Text: "trigger"}}} {
		result, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "insert", Values: values}})
		if err != nil || !result.CommitConfirmed || result.Outcomes[0].GeneratedID < 9007199254740993 || len(result.Outcomes[0].Saved.Original) != 4 {
			t.Fatal("native default/identity output", err, result)
		}
		row, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, result.Outcomes[0].Saved.Ref)
		if err != nil {
			t.Fatal(err)
		}
		want := "DEFAULT"
		if v, ok := values["V"]; ok && !v.Default {
			want = "TRIGGER"
		}
		if row.Values["V"].Text != want || row.Values["N"].Text != "1.23456789012345678901" || row.Values["G"].Text != row.Snapshot.Original[3].Text {
			t.Fatal("defaults or trigger results guessed", row.Values)
		}
		updated, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"V": {Default: true}}}})
		if err != nil || updated.Outcomes[0].Saved.Original[1].Text != "DEFAULT" {
			t.Fatal("native UPDATE DEFAULT", err, updated)
		}
	}
}

type editReceiptConnector struct {
	driver.Connector
	armed              atomic.Bool
	commits, mutations atomic.Int64
	beforeExec         func(string)
	afterExec          func(string, error)
}
type editReceiptConn struct {
	driver.Conn
	owner *editReceiptConnector
}
type editReceiptTx struct {
	driver.Tx
	owner *editReceiptConnector
}

func (c *editReceiptConnector) Connect(ctx context.Context) (driver.Conn, error) {
	x, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &editReceiptConn{Conn: x, owner: c}, nil
}
func (c *editReceiptConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}
func (c *editReceiptConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "BEGIN INSERT ") || strings.HasPrefix(q, "BEGIN UPDATE ") || strings.HasPrefix(q, "BEGIN DELETE ") {
		c.owner.mutations.Add(1)
	}
	if c.owner.beforeExec != nil {
		c.owner.beforeExec(q)
	}
	r, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if c.owner.afterExec != nil {
		c.owner.afterExec(q, err)
	}
	return r, err
}
func (c *editReceiptConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	x, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &editReceiptTx{Tx: x, owner: c.owner}, nil
}
func (t *editReceiptTx) Commit() error {
	t.owner.commits.Add(1)
	err := t.Tx.Commit()
	if err == nil && t.owner.armed.CompareAndSwap(true, false) {
		return errors.Join(dpi.ErrOutcomeUnknown, dpi.ErrSessionClosed)
	}
	return err
}
func (c *editReceiptConn) CheckNamedValue(v *driver.NamedValue) error {
	return c.Conn.(driver.NamedValueChecker).CheckNamedValue(v)
}
func (c *editReceiptConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }
func (c *editReceiptConn) IsValid() bool                  { return c.Conn.(driver.Validator).IsValid() }
func (c *editReceiptConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func TestNativeDamengDataCommitReceiptLossRetainsCompleteResultWithoutReplay(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_EDIT_RECEIPT_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+"(ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(32))"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.ExecContext(context.Background(), "DROP TABLE "+q); err != nil {
			t.Error(err)
		}
	}()
	base, err := NewSessionConnector(c, password)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	fault := &editReceiptConnector{Connector: base}
	s.db = sql.OpenDB(fault)
	s.db.SetMaxOpenConns(1)
	fault.armed.Store(true)
	result, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"V": {Text: "once"}}}})
	if !errors.Is(err, contract.ErrDataCommitUncertain) || errors.Is(err, driver.ErrBadConn) || result == nil || result.CommitConfirmed || !result.CommitUncertain || result.Incomplete || len(result.Outcomes) != 1 || len(result.Saved) != 1 || result.Outcomes[0].Saved == nil {
		t.Fatal("lost native driver.Tx commit receipt", err, result)
	}
	if fault.commits.Load() != 1 || fault.mutations.Load() != 1 {
		t.Fatal("unknown commit replayed", fault.commits.Load(), fault.mutations.Load())
	}
	var count int
	var key int64
	if err = admin.QueryRowContext(ctx, "SELECT COUNT(*),MIN(ID) FROM "+q).Scan(&count, &key); err != nil || count != 1 || key != 9007199254740993 {
		t.Fatal("actual single native insertion", err, count, key)
	}
	verified, err := s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, result)
	if err != nil || verified.Status != "applied" || verified.Recovered == nil || !verified.Recovered.Verified || verified.Recovered.Outcomes[0].Saved.Ref.TypedValues[0].Text != "9007199254740993" {
		t.Fatal("explicit read-only recovery", err, verified)
	}
	if fault.commits.Load() != 1 || fault.mutations.Load() != 1 {
		t.Fatal("verification replayed the write")
	}
}

func observeEditSession(t *testing.T, s *Session, hook func(string, error)) *editReceiptConnector {
	t.Helper()
	c, err := connections.DamengProfileConfig(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	_, password := nativeFixture(t)
	base, err := NewSessionConnector(c, password)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &editReceiptConnector{Connector: base, afterExec: hook}
	s.db = sql.OpenDB(owner)
	s.db.SetMaxOpenConns(1)
	return owner
}

func TestNativeDamengDataSaveOneMiBLobsAndUnsafeTriggerRefusal(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	page, err := s.loadData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", contract.DataEditMaxBytes)
	bin := bytes.Repeat([]byte{0, 128, 255, 39}, contract.DataEditMaxBytes/4)
	result, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"C": {Text: text}, "L": {Binary: true, Bytes: bin}}}})
	if err != nil || result == nil || !result.CommitConfirmed || result.Outcomes[0].Saved.Original[5].Text != text || !bytes.Equal(result.Outcomes[0].Saved.Original[6].Bytes, bin) {
		t.Fatal("exact one MiB native save", err)
	}
	if !result.Outcomes[0].Previews[5].Truncated || len(result.Outcomes[0].Previews[5].Text) != 1024 || len(result.Outcomes[0].Previews[6].Value.Bytes) != 1024 {
		t.Fatal("large native save returned full preview")
	}
	// Independent SQL validates the lengths and native stored boundaries.
	var n, m int64
	var tail string
	if err = admin.QueryRowContext(ctx, "SELECT SYS.DBMS_LOB.GETLENGTH(C),SYS.DBMS_LOB.GETLENGTH(L),SYS.DBMS_LOB.SUBSTR(C,1,1048576) FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&n, &m, &tail); err != nil || n != contract.DataEditMaxBytes || m != n || tail != "x" {
		t.Fatal("independent native large save", err, n, m)
	}
	for _, v := range []map[string]contract.DataValue{{"C": {Text: text + "x"}}, {"L": {Binary: true, Bytes: append(bytes.Clone(bin), 1)}}} {
		if _, err = s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: result.Outcomes[0].Saved, Values: v}}); !errors.Is(err, contract.ErrDataEditLimit) {
			t.Fatal("oversize native save input accepted", err)
		}
	}
	trigger := quote(o.Schema) + "." + quote(o.Name+"_BIG")
	if _, err = admin.ExecContext(ctx, "CREATE TRIGGER "+trigger+" AFTER UPDATE ON "+qualified(o)+" FOR EACH ROW BEGIN DECLARE V CLOB; BEGIN SELECT C INTO V FROM "+qualified(o)+" WHERE ID=:NEW.ID FOR UPDATE; SYS.DBMS_LOB.WRITEAPPEND(V,1,'y'); END; END;"); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, result.Outcomes[0].Saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	owner := observeEditSession(t, s, nil)
	failed, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: fresh.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "must refuse"}}}})
	if !errors.Is(err, contract.ErrDataReadOnly) || failed == nil || failed.Applied != 0 || failed.CommitUncertain || len(failed.Outcomes) != 0 || owner.mutations.Load() != 0 {
		t.Fatal("unsafe native LOB trigger started a mutation", err, failed)
	}
	if err = admin.QueryRowContext(ctx, "SELECT SYS.DBMS_LOB.GETLENGTH(C),T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&n, &tail); err != nil || n != contract.DataEditMaxBytes || tail != row.Values["T"].Text {
		t.Fatal("refused native LOB trigger changed the row", err, n, tail)
	}
	// Independently reproduce the native limitation through an unmanaged
	// original transaction. Only this owned fixture is changed by the probe.
	tx, e := admin.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, "UPDATE "+qualified(o)+" SET T='direct probe' WHERE ID=9007199254740993"); e != nil {
		_ = tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if err = admin.QueryRowContext(ctx, "SELECT SYS.DBMS_LOB.GETLENGTH(C),T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&n, &tail); err != nil || n != contract.DataEditMaxBytes+1 || tail != row.Values["T"].Text {
		t.Fatal("native locator rollback limitation changed", err, n, tail)
	}
	t.Log("native trigger locator append survived rollback; scalar UPDATE rolled back", n)
}

func TestNativeDamengDataSaveRejectsInvalidBatchBeforeFirstMutation(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	page, err := s.loadData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[1])
	if err != nil {
		t.Fatal(err)
	}
	owner := observeEditSession(t, s, nil)
	for _, v := range []map[string]contract.DataValue{
		{"N": {Text: "1.123456789012345678901"}}, {"N": {Text: "1000000000000000000"}},
		{"D": {Text: "2026-11-07 12:34:56.123456789"}}, {"D": {Text: "2026-02-30 12:34:56"}},
		{"GRP": {Text: "2147483648"}}, {"GRP": {Null: true}}, {"ID": {Text: "9223372036854775808"}},
		{"G": {Text: "4"}}, {"missing": {Text: "x"}}, {"B": {Binary: true, Bytes: make([]byte, 8001)}},
		{"T": {Text: strings.Repeat("文", 8001)}}, {"T": {Text: string([]byte{255})}}, {"T": {Text: "x", Null: true}},
		{"T": {Default: true, Text: "x"}},
	} {
		failed, e := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{
			{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "must not start"}}},
			{Kind: "update", Original: other.Snapshot, Values: v},
		})
		if e == nil || failed == nil || failed.Applied != 0 || failed.CommitUncertain || owner.mutations.Load() != 0 {
			t.Fatal("invalid later value allowed the first write", e, failed)
		}
	}
	for _, change := range []func(*contract.RowSnapshot){
		func(v *contract.RowSnapshot) { v.Metadata = "another session" },
		func(v *contract.RowSnapshot) { v.Ref.Object.ProfileID++ },
		func(v *contract.RowSnapshot) { v.Ref.TypedValues[0].Text = "1" },
		func(v *contract.RowSnapshot) { v.Original[3].Truncated = true },
		func(v *contract.RowSnapshot) { v.Mode = "fingerprints" },
	} {
		snapshot := cloneDataSnapshot(row.Snapshot)
		change(snapshot)
		if _, e := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: snapshot, Values: map[string]contract.DataValue{"T": {Text: "invalid"}}}}); e == nil || owner.mutations.Load() != 0 {
			t.Fatal("foreign/incomplete snapshot caused a write", e)
		}
	}
	var stored string
	if err = admin.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&stored); err != nil || stored != row.Values["T"].Text {
		t.Fatal("refused input changed stored row", err)
	}
}

func TestNativeDamengDataSaveExclusiveLockAndCancelledAcknowledgement(t *testing.T) {
	s, admin, o, _, _ := nativeReadTable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page, err := s.loadData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	var checked atomic.Bool
	observeEditSession(t, s, func(q string, err error) {
		if strings.HasSuffix(q, " IN EXCLUSIVE MODE") && err == nil && checked.CompareAndSwap(false, true) {
			short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
			defer stop()
			_, e := admin.ExecContext(short, "UPDATE "+qualified(o)+" SET T='concurrent' WHERE ID=9007199254740993")
			if e == nil {
				t.Fatal("original save lock allowed independent mutation")
			}
			t.Log("independent mutation blocked before snapshot validation", e)
		}
	})
	result, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "locked save"}}}})
	if err != nil || !checked.Load() || !result.CommitConfirmed {
		t.Fatal("exclusive native save", err)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET T='after release' WHERE ID=9007199254740993"); err != nil {
		t.Fatal("save did not release original lock", err)
	}
	row, err = s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	defer func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}()
	owner := observeEditSession(t, s, func(q string, err error) {
		if strings.HasPrefix(q, "BEGIN UPDATE ") && err == nil {
			close(entered)
			<-release // Withhold acknowledgement of the captured original execution.
		}
	})
	short, stop := context.WithCancel(ctx)
	defer stop()
	type completion struct {
		result *contract.DataEditResult
		err    error
	}
	finished := make(chan completion, 1)
	go func() {
		r, e := s.saveData(short, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "cancelled"}}}})
		finished <- completion{r, e}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native update never entered", ctx.Err())
	}
	stop()
	select {
	case <-finished:
		t.Fatal("save returned before original driver acknowledgement")
	case <-time.After(100 * time.Millisecond):
	}
	if released.CompareAndSwap(false, true) {
		close(release)
	}
	var done completion
	select {
	case done = <-finished:
	case <-ctx.Done():
		t.Fatal("save did not settle after acknowledgement", ctx.Err())
	}
	if !errors.Is(done.err, context.Canceled) || done.result == nil || done.result.Applied != 0 || done.result.CommitUncertain || owner.commits.Load() != 0 || owner.mutations.Load() != 1 {
		t.Fatal("cancelled save did not confirm original rollback", done.err, done.result)
	}
	var stored string
	if err = admin.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&stored); err != nil || stored != "after release" {
		t.Fatal("cancelled native save survived rollback", err, stored)
	}
}

func TestNativeDamengDataSaveCancellationDuringOriginalTriggerRetainsUncertainDraft(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	owner := observeEditSession(t, s, nil)
	admin := nativeDB(t, c, password)
	o := s.Namespace("SYSDBA").Object("table", "NYADB_EDIT_CANCEL_"+nativeSuffix(t))
	if _, err := admin.ExecContext(context.Background(), "CREATE TABLE "+qualified(o)+"(ID BIGINT PRIMARY KEY,T VARCHAR(64))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP TABLE "+qualified(o)); err != nil {
			t.Error(err)
		}
	})
	if _, err := admin.ExecContext(context.Background(), "INSERT INTO "+qualified(o)+" VALUES(9007199254740993,'original')"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page, err := s.loadData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	trigger := quote(o.Schema) + "." + quote(o.Name+"_WAIT")
	if _, err = admin.ExecContext(ctx, "CREATE TRIGGER "+trigger+" BEFORE UPDATE ON "+qualified(o)+" FOR EACH ROW BEGIN SYS.DBMS_LOCK.SLEEP(5); END;"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = admin.QueryRowContext(ctx, `SELECT STATUS FROM SYS.ALL_OBJECTS WHERE OWNER=? AND OBJECT_NAME=? AND OBJECT_TYPE='TRIGGER'`, o.Schema, o.Name+"_WAIT").Scan(&status); err != nil || status != "VALID" {
		t.Fatal("native cancellation trigger did not compile", err, status)
	}
	baseline, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	baselineStarted := time.Now()
	if _, err = baseline.ExecContext(ctx, "UPDATE "+qualified(o)+" SET T='baseline' WHERE ID=9007199254740993"); err != nil {
		_ = baseline.Rollback()
		t.Fatal("native trigger baseline execution", err)
	}
	if err = baseline.Rollback(); err != nil {
		t.Fatal(err)
	}
	if time.Since(baselineStarted) < 4*time.Second {
		t.Fatal("native trigger baseline did not actually wait")
	}
	// Refresh metadata after installing the trigger.
	row, err = s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, *page.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithCancel(ctx)
	defer stop()
	// TLS/race startup and metadata can exceed 500 ms under concurrent fixture
	// load. Start cancellation at the captured original UPDATE, not at save
	// creation; otherwise the test only proves cancellation before mutation.
	var timer *time.Timer
	var started time.Time
	owner.beforeExec = func(q string) {
		if strings.HasPrefix(q, "BEGIN UPDATE ") {
			started = time.Now()
			timer = time.AfterFunc(500*time.Millisecond, stop)
		}
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	failed, err := s.saveData(short, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"T": {Text: "must not replay"}}}})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, contract.ErrDataCommitUncertain) || failed == nil || !failed.CommitUncertain || !failed.Incomplete || failed.CommitConfirmed || len(failed.Outcomes) != 1 || failed.Outcomes[0].Saved != nil || started.IsZero() || time.Since(started) > 4*time.Second || owner.mutations.Load() != 1 || owner.commits.Load() != 0 {
		t.Fatal("cancelled in-flight native mutation lost uncertain state", err, failed, time.Since(started))
	}
	var stored string
	// The original client call acknowledges cancellation promptly, but the
	// server's procedural sleep can retain its transaction lock until it wakes.
	// Keep the draft uncertain and only poll independent read-only evidence.
	for {
		err = admin.QueryRowContext(ctx, "SELECT T FROM "+qualified(o)+" WHERE ID=9007199254740993").Scan(&stored)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "code -608") || time.Since(started) > 12*time.Second {
			t.Fatal("cancelled trigger independent state did not settle", err)
		}
		t.Log("original server sleep still holds its captured lock", err)
		time.Sleep(100 * time.Millisecond)
	}
	if stored != row.Values["T"].Text {
		t.Fatal("cancelled trigger changed independent stored state")
	}
	verified, err := s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, failed)
	if err != nil || verified.Status != "indeterminate" || verified.Recovered != nil {
		t.Fatal("missing saved row claimed a commit result", err, verified)
	}
}

func TestNativeDamengDataSaveScalarStorageMatrixAndChangedCompositeKey(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_EDIT_TYPES_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+`(ID BIGINT,K DECIMAL(8,2),TI TINYINT,SI SMALLINT,I INT,R REAL,F FLOAT,DB DOUBLE,BITVAL BIT,DA DATE,TM TIME(6),TZ TIME(6) WITH TIME ZONE,TS TIMESTAMP(6),STZ TIMESTAMP(6) WITH TIME ZONE,CH CHAR(4),"BY" BINARY(4),TX TEXT,IM IMAGE,PRIMARY KEY(ID,K))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP TABLE "+q); err != nil {
			t.Error(err)
		}
	})
	values := map[string]contract.DataValue{
		"ID": {Text: "9007199254740993"}, "K": {Text: "123456.78"}, "TI": {Text: "-128"}, "SI": {Text: "-32768"}, "I": {Text: "-2147483648"},
		"R": {Text: "1.5"}, "F": {Text: "1.2345678901234567"}, "DB": {Text: "-1.2345678901234567"}, "BITVAL": {Text: "true"},
		"DA": {Text: "2026-11-07"}, "TM": {Text: "10:20:30.123456"}, "TZ": {Text: "10:20:30.123456 +08:00"},
		"TS": {Text: "2026-11-07 10:20:30.123456"}, "STZ": {Text: "2026-11-07 10:20:30.123456 -05:30"},
		"CH": {Text: "abcd"}, "BY": {Binary: true, Bytes: []byte{0, 128, 255, 39}}, "TX": {Text: "中文🙂 '"}, "IM": {Binary: true, Bytes: []byte{0, 128, 255}},
	}
	result, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "insert", Values: values}})
	if err != nil || result == nil || !result.CommitConfirmed {
		t.Fatal("native scalar type matrix insert", err, result)
	}
	row, err := s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, result.Outcomes[0].Saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range values {
		got := row.Values[name]
		if got.Text != want.Text || !bytes.Equal(got.Bytes, want.Bytes) || got.Null {
			t.Fatal("native scalar save changed exact value", name, got.Text, want.Text)
		}
	}
	updated, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: row.Snapshot, Values: map[string]contract.DataValue{"ID": {Text: "9007199254741999"}, "K": {Text: "-123456.78"}, "TI": {Text: "127"}, "SI": {Text: "32767"}, "I": {Text: "2147483647"}, "BITVAL": {Text: "false"}}}})
	if err != nil || updated == nil || !updated.CommitConfirmed || updated.Outcomes[0].Saved.Ref.TypedValues[1].Text != "-123456.78" {
		t.Fatal("actual changed composite key", err, updated)
	}
	verified, err := s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, updated)
	if err != nil || verified.Status != "applied" || verified.Recovered == nil {
		t.Fatal("changed composite key verification", err, verified)
	}
	if _, err = s.readDataRow(ctx, s.ProfileID(), o.Schema, o.Name, row.Snapshot.Ref); !errors.Is(err, contract.ErrDataConflict) {
		t.Fatal("old composite key still identifies saved row", err)
	}
}

func TestNativeDamengDataSaveFinalTriggerValuesAndIndeterminateVerification(t *testing.T) {
	c, password := nativeFixture(t)
	s := fixtureSession(t, c, password)
	admin := nativeDB(t, c, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_EDIT_FINAL_"+nativeSuffix(t))
	q := qualified(o)
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+q+"(ID BIGINT PRIMARY KEY,V VARCHAR(32))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP TABLE "+q); err != nil {
			t.Error(err)
		}
	})
	if _, err := admin.ExecContext(ctx, "INSERT INTO "+q+" VALUES(1,'one'),(2,'two')"); err != nil {
		t.Fatal(err)
	}
	page, err := s.loadData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 2})
	if err != nil || page.Editable == nil || !*page.Editable || len(page.RowSnapshots) != 2 || page.RowSnapshots[0] == nil || page.RowSnapshots[1] == nil {
		t.Fatal("complete bounded rows lost save snapshots", err, page)
	}
	noop, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"V": {Text: "one"}}}})
	if err != nil || !noop.CommitConfirmed {
		t.Fatal(err)
	}
	verified, err := s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, noop)
	if err != nil || verified.Status != "indeterminate" || verified.Recovered != nil {
		t.Fatal("no-op verification claimed a distinguishable write", err, verified)
	}
	batch, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{
		{Kind: "update", Original: noop.Outcomes[0].Saved, Values: map[string]contract.DataValue{"V": {Text: "saved one"}}},
		{Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"V": {Text: "saved two"}}},
	})
	if err != nil || !batch.CommitConfirmed {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+q+" SET V='one' WHERE ID=1"); err != nil {
		t.Fatal(err)
	}
	verified, err = s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, batch)
	if err != nil || verified.Status != "indeterminate" || !reflect.DeepEqual(verified.States, []string{"not_applied", "applied"}) || verified.Recovered != nil {
		t.Fatal("mixed native state claimed atomic recovery", err, verified)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+q+" SET V='two' WHERE ID=2"); err != nil {
		t.Fatal(err)
	}
	verified, err = s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, batch)
	if err != nil || verified.Status != "not_applied" || verified.Recovered != nil {
		t.Fatal("original values claimed saved recovery", err, verified)
	}
	trigger := quote(o.Schema) + "." + quote(o.Name+"_FINAL")
	if _, err = admin.ExecContext(ctx, "CREATE TRIGGER "+trigger+" AFTER UPDATE ON "+q+" FOR EACH ROW BEGIN IF :NEW.ID=2 THEN UPDATE "+q+" SET V='later trigger' WHERE ID=1; END IF; END;"); err != nil {
		t.Fatal(err)
	}
	page, err = s.loadData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	final, err := s.saveData(ctx, s.ProfileID(), o.Schema, o.Name, []contract.DataEdit{
		{Kind: "update", Original: page.RowSnapshots[0], Values: map[string]contract.DataValue{"V": {Text: "first update"}}},
		{Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"V": {Text: "second update"}}},
	})
	if err != nil || final == nil || !final.CommitConfirmed || final.Outcomes[0].Saved.Original[1].Text != "later trigger" || final.Saved[0].Values["V"].Text != "later trigger" {
		t.Fatal("earlier saved outcome missed later native trigger", err, final)
	}
}

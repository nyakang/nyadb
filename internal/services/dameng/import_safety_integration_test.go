package dameng

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func TestNativeDamengImportCommitReceiptLossRetainsOperationEvidenceWithoutReplay(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(32)")
	owner := observeDataTransactions(t, s)
	owner.armed.Store(true)
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"V": {Text: "once"}})}})
	if !errors.Is(err, contract.ErrDataCommitUncertain) || !errors.Is(err, dpi.ErrOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || out.Committed || !out.CommitUncertain || out.Saved == nil || out.Saved.Incomplete || out.Saved.CommitConfirmed || !out.Saved.CommitUncertain || out.Rows[0].Status != "uncertain" || out.Rows[0].Outcome.GeneratedID != 9007199254740993 || out.Rows[0].Outcome.Saved == nil {
		t.Fatal("lost original commit must preserve complete operation evidence", err, out)
	}
	var count, id int64
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*),MIN(ID) FROM "+qualified(o)).Scan(&count, &id); err != nil || count != 1 || id != 9007199254740993 || owner.mutations.Load() != 1 || owner.commits.Load() != 1 {
		t.Fatal("lost original commit was replayed", err, count, id)
	}
	if _, err = s.verifyData(ctx, s.ProfileID(), o.Schema, o.Name, out.Saved); err == nil {
		t.Fatal("private import ROWID accepted as ordinary editable recovery identity")
	}
}

type importFaultConnector struct {
	*transactionReceiptConnector
	after func(string, error) error
}
type importFaultConn struct {
	*transactionReceiptConn
	owner *importFaultConnector
}

func (c *importFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	v, err := c.transactionReceiptConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &importFaultConn{v.(*transactionReceiptConn), c}, nil
}
func (c *importFaultConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	r, err := c.transactionReceiptConn.ExecContext(ctx, q, args)
	return r, c.owner.after(q, err)
}
func injectImportFault(t *testing.T, s *Session, after func(string, error) error) *editReceiptConnector {
	t.Helper()
	owner := observeDataTransactions(t, s)
	if err := s.dataDB.Close(); err != nil {
		t.Fatal(err)
	}
	s.dataDB = sql.OpenDB(&importFaultConnector{&transactionReceiptConnector{owner}, after})
	s.dataDB.SetMaxOpenConns(1)
	return owner
}

func TestNativeDamengImportCreationReceiptLossAndExistingTargetProtection(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_IMPORT_CREATE_"+nativeSuffix(t))
	t.Cleanup(func() { s.db.ExecContext(context.Background(), "DROP TABLE "+qualified(o)) })
	creates := 0
	owner := injectImportFault(t, s, func(q string, err error) error {
		if strings.HasPrefix(q, "CREATE TABLE ") {
			creates++
			if err == nil {
				return errors.Join(dpi.ErrOutcomeUnknown, dpi.ErrSessionClosed)
			}
		}
		return err
	})
	r := contract.DataImportRequest{NewTable: true, Columns: []contract.DataImportColumn{{Name: "ID", Type: "INT"}}, Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}})}}
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, r)
	if !errors.Is(err, dpi.ErrOutcomeUnknown) || out.Created || !out.CreationUncertain || out.Committed || out.CommitUncertain || creates != 1 || owner.mutations.Load() != 0 {
		t.Fatal("unknown CREATE receipt guessed success or replayed", err, out)
	}
	var count int64
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 0 {
		t.Fatal("original unknown DDL was dropped", err, count)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(9)"); err != nil {
		t.Fatal(err)
	}
	out, err = s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, r)
	if err == nil || out.Created || out.CreationUncertain || out.Committed || out.CommitUncertain || creates != 2 || owner.mutations.Load() != 0 {
		t.Fatal("existing table overwritten", err, out)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT MIN(ID) FROM "+qualified(o)).Scan(&count); err != nil || count != 9 {
		t.Fatal("existing target contents changed", err, count)
	}
}

func TestNativeDamengImportPartialLOBFailureRollsBackOnlyFailedRow(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY,C CLOB,B BLOB")
	updates := 0
	failure := errors.New("injected acknowledged LOB error")
	owner := injectImportFault(t, s, func(q string, err error) error {
		if err == nil && strings.HasPrefix(q, "BEGIN UPDATE ") {
			updates++
			if updates == 2 {
				return failure
			}
		}
		return err
	})
	makeRow := func(id string) contract.DataEdit {
		return importEdit(map[string]contract.DataValue{"ID": {Text: id}, "C": {Text: "complete"}, "B": {Binary: true, Bytes: []byte{0, 128, 255}}})
	}
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{ContinueOnError: true, Edits: []contract.DataEdit{makeRow("1"), makeRow("2")}})
	if err != nil || !out.Committed || out.CommitUncertain || out.Saved.Applied != 1 || out.Rows[0].Status != "failed" || out.Rows[1].Status != "committed" || owner.commits.Load() != 1 {
		t.Fatal("partial LOB row continuation", err, out)
	}
	var count, id int64
	var c string
	var b []byte
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*),MIN(ID) FROM "+qualified(o)).Scan(&count, &id); err != nil || count != 1 || id != 2 {
		t.Fatal("failed row prefix survived savepoint", err, count, id)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT C,B FROM "+qualified(o)).Scan(&c, &b); err != nil || c != "complete" || len(b) != 3 || b[2] != 255 {
		t.Fatal("later complete LOB row lost", err)
	}
}

func TestNativeDamengImportSourceLengthCloseFailureAndOriginalBinding(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY")
	for _, stage := range []string{"preflight_short", "execute_short", "preflight_close", "execute_close", "foreign", "generation", "unsupported"} {
		t.Run(stage, func(t *testing.T) {
			opens, closes := 0, 0
			failure := errors.New("source close failed")
			r := contract.DataImportStreamRequest{Rows: 1, Open: func(context.Context) (contract.DataImportRowSource, error) {
				opens++
				source := &importGeneratedSource{total: 1, closed: &closes, value: func(int) contract.DataEdit { return importEdit(map[string]contract.DataValue{"ID": {Text: "1"}}) }}
				if stage == "preflight_short" || stage == "execute_short" && opens == 2 {
					source.total = 0
				}
				if stage == "preflight_close" || stage == "execute_close" && opens == 2 {
					source.closeErr = failure
				}
				return source, nil
			}}
			id := s.ProfileID()
			if stage == "foreign" {
				id++
			}
			if stage == "generation" {
				r.Generation = s.Generation() + 1
			}
			if stage == "unsupported" {
				r.IgnoreForeignKeys = true
			}
			out, err := s.importStream(ctx, id, o.Schema, o.Name, r)
			if err == nil || out.Committed || out.CommitUncertain || closes != opens {
				t.Fatal("source or binding failure committed or skipped close", err, out, opens, closes)
			}
			if strings.HasSuffix(stage, "close") && !errors.Is(err, failure) {
				t.Fatal("source Close error lost", err)
			}
			if (stage == "foreign" || stage == "generation" || stage == "unsupported") && opens != 0 {
				t.Fatal("invalid original binding opened caller source")
			}
			var count int64
			if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed source committed prefix", err, count)
			}
		})
	}
}

type importAcknowledgedSource struct {
	stage   string
	entered chan struct{}
	release chan struct{}
	index   int
}

func (s *importAcknowledgedSource) Next(ctx context.Context) (contract.DataEdit, error) {
	if s.stage == "next" && s.index == 0 {
		close(s.entered)
		<-s.release
		return contract.DataEdit{}, ctx.Err()
	}
	if s.index > 0 {
		return contract.DataEdit{}, io.EOF
	}
	s.index++
	return importEdit(map[string]contract.DataValue{"ID": {Text: "1"}}), nil
}
func (s *importAcknowledgedSource) Close() error {
	if s.stage == "close" {
		close(s.entered)
		<-s.release
	}
	return nil
}

func TestNativeDamengImportCancelAndSessionCloseWaitForOriginalAcknowledgement(t *testing.T) {
	for _, stage := range []string{"next", "close", "record", "dml"} {
		t.Run(stage, func(t *testing.T) {
			cfg, password := nativeFixture(t)
			s := fixtureSession(t, cfg, password)
			admin := nativeDB(t, cfg, password)
			ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
			defer stop()
			o := exportFixtureTable(t, s, "ID INT PRIMARY KEY")
			// Keep fixture cleanup independent of the closed original Session.
			defer admin.ExecContext(context.Background(), "DROP TABLE "+qualified(o))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			ack := func() { once.Do(func() { close(release) }) }
			defer ack()
			owner := observeDataTransactions(t, s)
			if stage == "dml" {
				owner.afterExec = func(q string, err error) {
					if err == nil && strings.HasPrefix(q, "BEGIN INSERT ") {
						close(entered)
						<-release
					}
				}
			}
			opens := 0
			r := contract.DataImportStreamRequest{Rows: 1, Open: func(context.Context) (contract.DataImportRowSource, error) {
				opens++
				which := ""
				if opens == 2 {
					which = stage
				}
				return &importAcknowledgedSource{stage: which, entered: entered, release: release}, nil
			}}
			if stage == "record" {
				r.Record = func(contract.DataImportRowResult) error { close(entered); <-release; return nil }
			}
			write, cancel := context.WithCancel(ctx)
			defer cancel()
			type completion struct {
				out *contract.DataImportStreamResult
				err error
			}
			done := make(chan completion, 1)
			go func() {
				out, err := s.importStream(write, s.ProfileID(), o.Schema, o.Name, r)
				done <- completion{out, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("original import did not enter", ctx.Err())
			}
			cancel()
			closed := make(chan error, 1)
			go func() { closed <- s.Close() }()
			select {
			case v := <-done:
				t.Fatal("import skipped original acknowledgement", v.err)
			case e := <-closed:
				t.Fatal("Session.Close skipped import acknowledgement", e)
			case <-time.After(100 * time.Millisecond):
			}
			ack()
			select {
			case v := <-done:
				if !errors.Is(v.err, context.Canceled) || v.out.Committed || v.out.CommitUncertain {
					t.Fatal("canceled acknowledged import committed", v.err, v.out)
				}
			case <-ctx.Done():
				t.Fatal("import failed to settle", ctx.Err())
			}
			select {
			case e := <-closed:
				if e != nil {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal("Session.Close failed to settle")
			}
			if owner.commits.Load() != 0 {
				t.Fatal("canceled import submitted commit")
			}
			var count int64
			if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 0 {
				t.Fatal("canceled original import prefix persisted", err, count)
			}
			if _, err := s.importStream(ctx, s.ProfileID(), o.Schema, o.Name, r); !errors.Is(err, contract.ErrRevoked) {
				t.Fatal("closed session accepted recovery import", err)
			}
		})
	}
}

func TestNativeDamengImportCompositeUniqueKeysAndRejectedKeys(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "A DECIMAL(38,20) NOT NULL,B VARBINARY(8) NOT NULL,V VARCHAR(32),UNIQUE(A,B)")
	value := importEdit(map[string]contract.DataValue{"A": {Kind: contract.Decimal, Text: "123456789012345678.12345678901234567891"}, "B": {Binary: true, Bytes: []byte{0, 128, 255}}, "V": {Text: "old"}})
	if _, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Edits: []contract.DataEdit{value}}); err != nil {
		t.Fatal(err)
	}
	value.Values["V"] = contract.DataValue{Text: "new"}
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "upsert", MatchColumns: []string{"B", "A"}, Edits: []contract.DataEdit{value}})
	if err != nil || !out.Committed || out.Rows[0].Kind != "update" {
		t.Fatal("complete selected exact composite key", err, out)
	}
	for _, keys := range [][]string{{"A"}, {"A", "A"}, {"V"}, {"missing"}} {
		out, err = s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "update", MatchColumns: keys, Edits: []contract.DataEdit{value}})
		if err == nil || out.Committed || out.CommitUncertain {
			t.Fatal("partial/duplicate/nonunique key accepted", keys, err)
		}
	}
	value.Values["A"] = contract.DataValue{Null: true}
	out, err = s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "upsert", MatchColumns: []string{"A", "B"}, ContinueOnError: true, Edits: []contract.DataEdit{value}})
	if !errors.Is(err, contract.ErrDataReadOnly) || out.Committed {
		t.Fatal("null matching key continued", err)
	}
	nullable := exportFixtureTable(t, s, "ID INT UNIQUE,V VARCHAR(32)")
	out, err = s.importRows(ctx, s.ProfileID(), nullable.Schema, nullable.Name, contract.DataImportRequest{Mode: "upsert", MatchColumns: []string{"ID"}, Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}, "V": {Text: "x"}})}})
	if !errors.Is(err, contract.ErrDataReadOnly) || out.Committed {
		t.Fatal("nullable unique key accepted", err)
	}
}

func TestNativeDamengImportGrantedAccountAndDeniedObject(t *testing.T) {
	cfg, password := nativeFixture(t)
	admin := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := exportFixtureTable(t, admin, "ID INT PRIMARY KEY,C CLOB,B BLOB")
	hidden := exportFixtureTable(t, admin, "ID INT")
	user, secret := "NYADB_IMPORT_USER_"+strings.ToUpper(nativeSuffix(t)), "Aa9!"+nativeSuffix(t)
	if _, err := admin.db.ExecContext(ctx, "CREATE USER "+quote(user)+" IDENTIFIED BY "+quote(secret)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.db.ExecContext(context.Background(), "DROP USER "+quote(user)+" CASCADE") })
	if _, err := admin.db.ExecContext(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON "+qualified(o)+" TO "+quote(user)); err != nil {
		t.Fatal(err)
	}
	cfg.Username = user
	cfg = accountCertificate(t, cfg)
	s := fixtureSession(t, cfg, secret)
	value := importEdit(map[string]contract.DataValue{"ID": {Text: "1"}, "C": {Text: "中文"}, "B": {Binary: true, Bytes: []byte{0, 128, 255}}})
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Edits: []contract.DataEdit{value}})
	if err != nil || !out.Committed || out.Rows[0].Outcome.Saved.Original[1].Text != "中文" {
		t.Fatal("ordinary granted import", err, out)
	}
	value.Values["C"] = contract.DataValue{Text: "updated"}
	out, err = s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "update", MatchColumns: []string{"ID"}, Edits: []contract.DataEdit{value}})
	if err != nil || !out.Committed || out.Rows[0].Outcome.Saved.Original[1].Text != "updated" {
		t.Fatal("ordinary granted unique-key update", err, out)
	}
	opens := 0
	stream := contract.DataImportStreamRequest{Rows: 1, Open: func(context.Context) (contract.DataImportRowSource, error) {
		opens++
		return &importRowsSource{edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}})}}, nil
	}}
	denied, err := s.importStream(ctx, s.ProfileID(), hidden.Schema, hidden.Name, stream)
	if err == nil || denied.Committed || opens != 0 {
		t.Fatal("denied object opened source or wrote", err)
	}
}

func TestNativeDamengImportNativeDMLCancellationRetainsIncompleteUncertainty(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY,V VARCHAR(32)")
	if _, err := s.db.ExecContext(ctx, "CREATE TRIGGER "+quote(o.Schema)+"."+quote(o.Name+"_TR")+" BEFORE INSERT ON "+qualified(o)+" FOR EACH ROW BEGIN SYS.DBMS_LOCK.SLEEP(5); END;"); err != nil {
		t.Fatal(err)
	}
	owner := observeDataTransactions(t, s)
	entered := make(chan struct{})
	owner.beforeExec = func(q string) {
		if strings.HasPrefix(q, "BEGIN INSERT ") {
			close(entered)
		}
	}
	write, cancel := context.WithCancel(ctx)
	defer cancel()
	type completion struct {
		out *contract.DataImportResult
		err error
	}
	done := make(chan completion, 1)
	go func() {
		out, err := s.importRows(write, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{ContinueOnError: true, Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}, "V": {Text: "must not replay"}})}})
		done <- completion{out, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native import never entered")
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case v := <-done:
		if !errors.Is(v.err, context.Canceled) || !errors.Is(v.err, contract.ErrDataCommitUncertain) || !v.out.CommitUncertain || v.out.Committed || v.out.Saved == nil || !v.out.Saved.Incomplete || v.out.Rows[0].Status != "uncertain" {
			t.Fatal("native cancellation lost incomplete write state", v.err, v.out)
		}
	case <-ctx.Done():
		t.Fatal("native import cancellation never acknowledged")
	}
	if owner.mutations.Load() != 1 || owner.commits.Load() != 0 {
		t.Fatal("native canceled import replayed or committed")
	}
	started := time.Now()
	var count int64
	for {
		err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "code -608") || time.Since(started) > 12*time.Second {
			t.Fatal("independent canceled import read", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if count != 0 {
		t.Fatal("canceled native import persisted", count)
	}
}

func TestNativeDamengImportPreSubmissionCancellationSettlesOriginalRollback(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY")
	owner := observeDataTransactions(t, s)
	write, cancel := context.WithCancel(ctx)
	defer cancel()
	owner.beforeExec = func(q string) {
		if strings.HasPrefix(q, "BEGIN INSERT ") {
			cancel()
		}
	}
	out, err := s.importRows(write, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}})}})
	if !errors.Is(err, context.Canceled) || errors.Is(err, dpi.ErrOutcomeUnknown) || out.Committed || out.CommitUncertain || out.Saved != nil || len(out.Rows) != 1 || out.Rows[0].Status != "rolled_back" || owner.commits.Load() != 0 {
		t.Fatal("unsubmitted cancellation ignored acknowledged original rollback", err, out)
	}
	var count int64
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 0 {
		t.Fatal("unsubmitted cancellation changed original table", err, count)
	}
}

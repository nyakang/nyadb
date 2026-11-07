package dameng

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/dameng/dpi"
)

func exportFixtureTable(t *testing.T, s *Session, columns string) contract.ObjectRef {
	t.Helper()
	o := s.Namespace("SYSDBA").Object("table", "NYADB_EXPORT_"+nativeSuffix(t))
	if _, err := s.db.ExecContext(context.Background(), "CREATE TABLE "+qualified(o)+"("+columns+")"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.ExecContext(context.Background(), "DROP TABLE "+qualified(o)) })
	return o
}

func TestNativeDamengExportPreservesPhysicalDuplicatesCompleteLOBsAndCallbackOwnership(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	o := exportFixtureTable(t, s, "ID INT,C CLOB,B BLOB")
	large := strings.Repeat("x", contract.DataExportFieldMaxBytes)
	bin := bytes.Repeat([]byte{0, 128, 255}, contract.DataExportFieldMaxBytes/3)
	if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(1,?,?)", large, bin); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(2,'duplicate',HEXTORAW('0080FF'))"); err != nil {
			t.Fatal(err)
		}
	}
	r := contract.ComposeData(s)
	if r.Exporter == nil || r.JSONL == nil || !r.Capabilities.Export || !r.Capabilities.JSONL {
		t.Fatal("native exports missing from shared contract")
	}
	var kept [][]contract.DataValue
	err := r.Exporter(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Object: o, SortColumn: "ID"}, []string{"B", "C", "ID"}, func(row []contract.DataValue) error {
		if len(row) != 3 || row[0].Kind != contract.Binary || row[1].Kind != contract.Text || row[2].Kind != contract.Integer {
			t.Fatal("selected order or types changed")
		}
		if len(kept) == 0 {
			if row[1].Text != large || !bytes.Equal(row[0].Bytes, bin) || row[2].Text != "1" {
				t.Fatal("export emitted a preview or changed the original LOB")
			}
		} else if row[1].Text != "duplicate" || !bytes.Equal(row[0].Bytes, []byte{0, 128, 255}) || row[2].Text != "2" {
			t.Fatal("duplicate row lost original identity")
		}
		kept = append(kept, row)
		row[0].Bytes[0] = byte(len(kept))
		return nil
	})
	if err != nil || len(kept) != 4 {
		t.Fatal("no-primary-key physical export failed", len(kept), err)
	}
	for i, row := range kept {
		if row[0].Bytes[0] != byte(i+1) {
			t.Fatal("callbacks shared buffers across records")
		}
	}
	var raw bytes.Buffer
	if err = r.JSONL(ctx, o, &raw); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&raw)
	for i := range 4 {
		var row []contract.DataValue
		if err = decoder.Decode(&row); err != nil || len(row) != 3 || row[2].Bytes[0] != 0 {
			t.Fatal("JSONL changed native source or column order", i, err)
		}
	}
	if err = decoder.Decode(new([]contract.DataValue)); !errors.Is(err, io.EOF) {
		t.Fatal("duplicate records omitted or added", err)
	}
}

func TestNativeDamengExportScalarTypesQuotedNamesAndEmptyTable(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	name := `列 "';--`
	o := exportFixtureTable(t, s, `ID BIGINT,N DECIMAL(38,20),F DOUBLE,Q BIT,C CHAR(12),`+quote(name)+` VARCHAR(128),B BINARY(4),V VARBINARY(4),D DATE,T TIME(6),Z TIMESTAMP(6) WITH TIME ZONE`)
	called := 0
	sink := func([]contract.DataValue) error { called++; return nil }
	if err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, nil, sink); err != nil || called != 0 {
		t.Fatal("empty table export failed", err)
	}
	text := `中文 ' \ %_ <>&`
	if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(?,?,?,?,'xy',?,HEXTORAW('0080FF27'),HEXTORAW('0080FF'),DATE '2026-11-07',TIME '12:34:56.123456',TIMESTAMP '2026-11-07 12:34:56.123456 +08:00')", int64(9007199254740993), dpi.Decimal("123456789012345678.12345678901234567891"), 1.25, true, text); err != nil {
		t.Fatal(err)
	}
	err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{FilterColumn: name, FilterMode: "equal", FilterValue: text, SortColumn: name}, nil, func(row []contract.DataValue) error {
		called++
		if len(row) != 11 || row[0].Kind != contract.Integer || row[0].Text != "9007199254740993" || row[1].Kind != contract.Decimal || row[1].Text != "123456789012345678.12345678901234567891" || row[2].Kind != contract.Floating || row[2].Text != "1.25" || row[3].Kind != contract.Boolean || row[3].Text != "true" || strings.TrimRight(row[4].Text, " ") != "xy" || row[5].Text != text || !bytes.Equal(row[6].Bytes, []byte{0, 128, 255, 39}) || !bytes.Equal(row[7].Bytes, []byte{0, 128, 255}) || row[8].Kind != contract.DateTime || row[9].Kind != contract.DateTime || row[10].Kind != contract.DateTime || !strings.Contains(row[10].Text, "+08:00") {
			t.Fatal("native scalar type export changed", row)
		}
		return nil
	})
	if err != nil || called != 1 {
		t.Fatal("quoted scalar export failed", err)
	}
	called = 0
	for _, options := range []contract.DataOptions{{FilterColumn: "C", FilterMode: "contains", FilterValue: "x"}, {SortColumn: "missing"}, {SortColumn: "B"}} {
		if err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, options, nil, sink); err == nil || called != 0 {
			t.Fatal("unsupported/invalid semantics accepted", err)
		}
	}
}

func TestNativeDamengExportSelectOnlyAccountAndDeniedObject(t *testing.T) {
	cfg, password := nativeFixture(t)
	admin := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	o := exportFixtureTable(t, admin, "ID INT,C CLOB,B BLOB")
	hidden := exportFixtureTable(t, admin, "ID INT")
	if _, err := admin.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(1,'中文',HEXTORAW('0080FF'))"); err != nil {
		t.Fatal(err)
	}
	user, secret := "NYADB_EXPORT_USER_"+strings.ToUpper(nativeSuffix(t)), "Aa9!"+nativeSuffix(t)
	if _, err := admin.db.ExecContext(ctx, "CREATE USER "+quote(user)+" IDENTIFIED BY "+quote(secret)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.db.ExecContext(context.Background(), "DROP USER "+quote(user)+" CASCADE") })
	if _, err := admin.db.ExecContext(ctx, "GRANT SELECT ON "+qualified(o)+" TO "+quote(user)); err != nil {
		t.Fatal(err)
	}
	cfg.Username = user
	cfg = accountCertificate(t, cfg)
	s := fixtureSession(t, cfg, secret)
	called := 0
	err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, nil, func(row []contract.DataValue) error {
		called++
		if row[1].Text != "中文" || !bytes.Equal(row[2].Bytes, []byte{0, 128, 255}) {
			t.Fatal("SELECT-only export changed values")
		}
		return nil
	})
	if err != nil || called != 1 {
		t.Fatal("SELECT-only native export failed", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE "+qualified(o)+" SET ID=2"); err == nil {
		t.Fatal("SELECT-only account gained writes")
	}
	err = s.exportData(ctx, s.ProfileID(), hidden.Schema, hidden.Name, contract.DataOptions{}, nil, func([]contract.DataValue) error { t.Fatal("export exposed denied object"); return nil })
	if err == nil {
		t.Fatal("export accepted denied object")
	}
}

func TestNativeDamengExportTypedValuesFiltersOffsetsAndQualifiedIdentity(t *testing.T) {
	s, _, o, text, bin := nativeReadTable(t)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	var exported [][]contract.DataValue
	err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Object: o}, nil, func(row []contract.DataValue) error {
		exported = append(exported, row)
		return nil
	})
	if err != nil || len(exported) != 4 {
		t.Fatal("full typed export failed", err)
	}
	for i, row := range exported {
		if len(row) != 11 || row[0].Text != fmt.Sprint(int64(9007199254740993)+int64(i)) || row[2].Kind != contract.Decimal || row[2].Text != "123456789012345678.12345678901234567891" || row[10].Kind != contract.DateTime || row[9].Kind != contract.Integer {
			t.Fatal("typed numeric/temporal/generated values changed", i, row)
		}
	}
	if exported[0][5].Null || exported[0][5].Text != "" || exported[0][6].Null || !exported[0][6].Binary || len(exported[0][6].Bytes) != 0 || exported[1][5].Text != text || !bytes.Equal(exported[1][6].Bytes, bin) || !exported[1][7].Null || !exported[3][5].Null || !exported[3][6].Null || exported[2][3].Text != strings.Repeat("文", 1400) {
		t.Fatal("empty/null/Unicode/LOB export changed")
	}
	var ids []string
	options := contract.DataOptions{Object: o, FilterColumn: "ID", FilterMode: "greater", FilterValue: "9007199254740993", SortColumn: "ID", SortDescending: true, Offset: 1, Limit: 1}
	err = s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, options, []string{"ID"}, func(row []contract.DataValue) error { ids = append(ids, row[0].Text); return nil })
	if err != nil || !reflect.DeepEqual(ids, []string{"9007199254740995"}) {
		t.Fatal("filter/order/offset/limit export changed", ids, err)
	}
	options.Limit = 0
	ids = nil
	if err = s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, options, []string{"ID"}, func(row []contract.DataValue) error { ids = append(ids, row[0].Text); return nil }); err != nil || len(ids) != 2 {
		t.Fatal("unlimited offset export failed", ids, err)
	}
	called := false
	sink := func([]contract.DataValue) error { called = true; return nil }
	for _, foreign := range []contract.ObjectRef{{ProfileID: s.ProfileID() + 1, Catalog: o.Catalog, Schema: o.Schema, Kind: o.Kind, Name: o.Name}, {ProfileID: s.ProfileID(), Catalog: "foreign", Schema: o.Schema, Kind: o.Kind, Name: o.Name}} {
		if err = s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Object: foreign}, nil, sink); !errors.Is(err, connections.ErrInvalid) || called {
			t.Fatal("export crossed qualified identity", err)
		}
	}
	if err = s.exportData(ctx, s.ProfileID()+1, o.Schema, o.Name, contract.DataOptions{}, nil, sink); !errors.Is(err, connections.ErrInvalid) || called {
		t.Fatal("export accepted another profile", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, nil, sink); !errors.Is(err, contract.ErrRevoked) || called {
		t.Fatal("closed original export reopened a session", err)
	}
}

type exportFieldCounter struct {
	*sql.Conn
	reads      int
	beforeRead func()
}

func (q *exportFieldCounter) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.HasPrefix(query, "SELECT \"") {
		q.reads++
		if q.beforeRead != nil {
			q.beforeRead()
		}
	}
	return q.Conn.QueryRowContext(ctx, query, args...)
}

func TestNativeDamengExportByteLimitsRejectBeforeLOBMaterialization(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	o := exportFixtureTable(t, s, "ID INT,C CLOB,B BLOB")
	text := strings.Repeat("文", contract.DataExportFieldMaxBytes/3)
	bin := bytes.Repeat([]byte{0x80}, contract.DataExportFieldMaxBytes)
	if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(1,?,?)", text, bin); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, []string{"C", "B"}, func(row []contract.DataValue) error {
		count++
		if row[0].Text != text || !bytes.Equal(row[1].Bytes, bin) {
			t.Fatal("within-bound multibyte/binary export changed")
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatal("exact binary byte limit rejected", err)
	}
	for _, extend := range []string{"UPDATE " + qualified(o) + " SET C=C||'文'", "UPDATE " + qualified(o) + " SET C=EMPTY_CLOB()", "DECLARE V BLOB; BEGIN SELECT B INTO V FROM " + qualified(o) + " FOR UPDATE; SYS.DBMS_LOB.WRITEAPPEND(V,1,HEXTORAW('80')); END;"} {
		if _, err := s.db.ExecContext(ctx, extend); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(extend, "EMPTY_CLOB") {
			continue
		}
		count = 0
		err := s.completeDataRead(ctx, o, func(ctx context.Context, c *sql.Conn, table *contract.TableStructure) error {
			hook := &exportFieldCounter{Conn: c}
			e := exportRowsAt(ctx, hook, o, table.Columns, "SELECT ROWID FROM "+qualified(o), nil, 0, func([]contract.DataValue) error { count++; return nil })
			if hook.reads != 0 {
				t.Fatal("oversized field materialized before byte guard")
			}
			return e
		})
		if !errors.Is(err, contract.ErrDataEditLimit) || count != 0 {
			t.Fatal("oversized export leaked partial row", err)
		}
	}
	count = 0
	if err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, []string{"ID"}, func([]contract.DataValue) error { count++; return nil }); err != nil || count != 1 {
		t.Fatal("unselected oversized fields prevented valid projection", err)
	}
}

func TestNativeDamengExportRowLimitAndEscapedWorkerFrames(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 120*time.Second)
	defer stop()
	var declarations, names []string
	for i := range 17 {
		name := fmt.Sprintf("C%d", i)
		names = append(names, name)
		declarations = append(declarations, name+" CLOB")
	}
	o := exportFixtureTable(t, s, strings.Join(declarations, ","))
	if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+"(C0) VALUES(EMPTY_CLOB())"); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("\n", contract.DataExportFieldMaxBytes)
	for _, name := range names {
		if _, err := s.db.ExecContext(ctx, "UPDATE "+qualified(o)+" SET "+quote(name)+"=?", text); err != nil {
			t.Fatal(err)
		}
	}
	called := 0
	err := s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, names[:16], func(row []contract.DataValue) error {
		called++
		for _, field := range row {
			if field.Text != text {
				t.Fatal("complete escaped row changed")
			}
		}
		return nil
	})
	if err != nil || called != 1 {
		t.Fatal("exact 16 MiB row rejected by worker frame", err)
	}
	called = 0
	err = s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, names, func([]contract.DataValue) error { called++; return nil })
	if !errors.Is(err, contract.ErrDataEditLimit) || called != 0 {
		t.Fatal("oversized row emitted partial values", err)
	}
}

func TestNativeDamengExportOriginalReadLockCancellationSinkFailureAndSessionClose(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	admin := nativeDB(t, cfg, password)
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	o := exportFixtureTable(t, s, "ID INT,C CLOB,B BLOB")
	for i := range 2 {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(?, 'original', HEXTORAW('0080FF'))", int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	child, cancel := context.WithCancel(ctx)
	called := 0
	err := s.exportData(child, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, nil, func([]contract.DataValue) error {
		called++
		short, done := context.WithTimeout(ctx, 150*time.Millisecond)
		defer done()
		if _, e := admin.ExecContext(short, "UPDATE "+qualified(o)+" SET C='replacement'"); !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal("export did not block concurrent LOB replacement", e)
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || called != 1 {
		t.Fatal("export cancellation skipped original cleanup or kept streaming", err, called)
	}
	if _, err = admin.ExecContext(ctx, "UPDATE "+qualified(o)+" SET C='after'"); err != nil {
		t.Fatal("export retained native lock after return", err)
	}
	failed := errors.New("output rejected")
	called = 0
	err = s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, nil, func([]contract.DataValue) error { called++; return failed })
	if !errors.Is(err, failed) || called != 1 {
		t.Fatal("sink failure was replayed or suppressed", err)
	}
	err = s.completeDataRead(ctx, o, func(ctx context.Context, c *sql.Conn, table *contract.TableStructure) error {
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		hook := &exportFieldCounter{Conn: c, beforeRead: cancel}
		return exportRowsAt(child, hook, o, table.Columns, "SELECT ROWID FROM "+qualified(o), nil, 0, func([]contract.DataValue) error { t.Fatal("canceled field leaked a partial row"); return nil })
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("field cancellation was lost", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done, closed := make(chan error, 1), make(chan error, 1)
	go func() {
		done <- s.exportData(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{}, nil, func([]contract.DataValue) error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("export callback did not enter")
	}
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
		t.Fatal("session closed before original export callback ended")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("session close lost export cancellation", err)
	}
	if err = <-closed; err != nil {
		t.Fatal(err)
	}
}

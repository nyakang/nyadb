package dameng

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func importEdit(values map[string]contract.DataValue) contract.DataEdit {
	return contract.DataEdit{Kind: "insert", Values: values}
}
func importScalarRow(id int, s, v string) contract.DataEdit {
	return importEdit(map[string]contract.DataValue{"ID": {Text: fmt.Sprint(id)}, "S": {Text: s}, "V": {Text: v}})
}

func TestNativeDamengImportAllModesAndSelectedUniqueKey(t *testing.T) {
	for _, mode := range []string{"append", "replace", "update", "upsert", "insert_missing", "delete"} {
		t.Run(mode, func(t *testing.T) {
			cfg, password := nativeFixture(t)
			s := fixtureSession(t, cfg, password)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			o := exportFixtureTable(t, s, "ID INT PRIMARY KEY,S VARCHAR(32) NOT NULL UNIQUE,V VARCHAR(32)")
			if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(1,'A','old1'),(2,'B','old2')"); err != nil {
				t.Fatal(err)
			}
			r := contract.DataImportRequest{Mode: mode, Edits: []contract.DataEdit{importScalarRow(1, "A", "new1"), importScalarRow(3, "C", "new3")}}
			if mode == "append" || mode == "replace" {
				r.Edits[0] = importScalarRow(4, "D", "new4")
			} else {
				r.MatchColumns = []string{"ID"}
			}
			out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, r)
			if err != nil || out == nil || !out.Committed || out.CommitUncertain || out.Created || len(out.Rows) != 2 || out.Saved == nil || !out.Saved.CommitConfirmed {
				t.Fatal("native import mode", err, out)
			}
			var ids []int
			var values []string
			rows, err := s.db.QueryContext(ctx, "SELECT ID,V FROM "+qualified(o)+" ORDER BY ID")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id int
				var v string
				if err = rows.Scan(&id, &v); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
				values = append(values, v)
			}
			if err = errors.Join(rows.Err(), rows.Close()); err != nil {
				t.Fatal(err)
			}
			want := map[string][]int{"append": {1, 2, 3, 4}, "replace": {3, 4}, "update": {1, 2}, "upsert": {1, 2, 3}, "insert_missing": {1, 2, 3}, "delete": {2}}[mode]
			if !reflect.DeepEqual(ids, want) {
				t.Fatal("mode changed unrelated rows", ids, want)
			}
			if mode == "update" || mode == "upsert" {
				if values[0] != "new1" {
					t.Fatal("matched update lost values")
				}
			}
			if mode == "insert_missing" && values[0] != "old1" {
				t.Fatal("insert_missing overwrote existing row")
			}
			if mode == "update" || mode == "delete" {
				if out.Rows[1].Kind != "skip" || out.Rows[1].Status != "skipped" {
					t.Fatal("missing match not skipped")
				}
			}
			if mode == "insert_missing" && out.Rows[0].Status != "skipped" {
				t.Fatal("existing match not skipped")
			}
		})
	}
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY,S VARCHAR(32) NOT NULL UNIQUE,V VARCHAR(32)")
	if _, err := s.db.ExecContext(ctx, "INSERT INTO "+qualified(o)+" VALUES(1,'A','old')"); err != nil {
		t.Fatal(err)
	}
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "upsert", MatchColumns: []string{"S"}, Edits: []contract.DataEdit{importScalarRow(2, "A", "actual")}})
	if err != nil || !out.Committed || out.Rows[0].Kind != "update" {
		t.Fatal("standalone chosen text unique key", err)
	}
	var id int
	var value string
	if err = s.db.QueryRowContext(ctx, "SELECT ID,V FROM "+qualified(o)).Scan(&id, &value); err != nil || id != 2 || value != "actual" {
		t.Fatal("chosen text key did not match original row", err, id, value)
	}
	out, err = s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "upsert", MatchColumns: []string{"ID"}, Edits: []contract.DataEdit{importScalarRow(3, "A", "must fail")}})
	if err == nil || out.Committed || out.CommitUncertain {
		t.Fatal("unselected unique conflict updated another row", err)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT ID,V FROM "+qualified(o)).Scan(&id, &value); err != nil || id != 2 || value != "actual" {
		t.Fatal("unselected collision mutated unrelated row", err)
	}
}

func TestNativeDamengImportCreatesTypedTableAfterCompleteSourcePreflight(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	o := s.Namespace("SYSDBA").Object("table", `NYADB_Import "`+nativeSuffix(t))
	t.Cleanup(func() { s.db.ExecContext(context.Background(), "DROP TABLE "+qualified(o)) })
	cols := []contract.DataImportColumn{{Name: "ID", Type: "BIGINT", PrimaryKey: true}, {Name: `租户 "';--`, Type: "VARCHAR(64)", PrimaryKey: true}, {Name: "N", Type: "DECIMAL(38,20)", Nullable: true}, {Name: "C", Type: "CLOB", Nullable: true}, {Name: "B", Type: "BLOB", Nullable: true}, {Name: "D", Type: "TIMESTAMP(6)", Nullable: true}, {Name: "Q", Type: "BOOLEAN", Nullable: true}}
	text := strings.Repeat("中文🙂", 1800)
	bin := bytes.Repeat([]byte{0, 128, 255}, 1800)
	values := map[string]contract.DataValue{"ID": {Kind: contract.Integer, Text: "9007199254740993"}, cols[1].Name: {Kind: contract.Text, Text: `中文 ' ? \`}, "N": {Kind: contract.Decimal, Text: "123456789012345678.12345678901234567891"}, "C": {Kind: contract.Text, Text: text}, "B": {Kind: contract.Binary, Bytes: bin}, "D": {Kind: contract.DateTime, Text: "2026-11-07 12:34:56.123456"}, "Q": {Kind: contract.Boolean, Text: "true"}}
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{NewTable: true, Columns: cols, Edits: []contract.DataEdit{importEdit(values)}})
	if err != nil || !out.Created || out.CreationUncertain || !out.Committed || out.Saved == nil || out.Saved.Applied != 1 || len(out.Saved.Outcomes) != 1 {
		t.Fatal("typed native new-table import", err, out)
	}
	var id int64
	var n, c string
	var b []byte
	var q int64
	if err = s.db.QueryRowContext(ctx, "SELECT ID,N,C,B,Q FROM "+qualified(o)).Scan(&id, &n, &c, &b, &q); err != nil || id != 9007199254740993 || n != values["N"].Text || c != text || !bytes.Equal(b, bin) || q != 1 {
		t.Fatal("typed import changed exact values", err)
	}
	meta, err := s.TableMetadata(ctx, o)
	if err != nil || len(meta.Columns) != 7 || meta.Columns[0].Key != "PRI" || meta.Columns[1].Key != "PRI" {
		t.Fatal("native composite target DDL", err)
	}
	if out.Rows[0].Outcome.Saved.Ref.Object != o || out.Rows[0].Outcome.Saved.Ref.Columns[0] != "ROWID" || len(out.Rows[0].Outcome.Saved.Original) != 7 {
		t.Fatal("native qualified complete row receipt missing")
	}
	out.Rows[0].Outcome.Saved.Original[4].Bytes[0] = 99
	if out.Saved.Outcomes[0].Saved.Original[4].Bytes[0] != 0 {
		t.Fatal("row and saved receipt share mutable buffers")
	}
	failed := s.Namespace("SYSDBA").Object("table", "NYADB_IMPORT_INVALID_"+nativeSuffix(t))
	r := contract.DataImportRequest{NewTable: true, Columns: cols, Edits: []contract.DataEdit{importEdit(values), importEdit(map[string]contract.DataValue{"ID": {Text: "not a number"}})}}
	out, err = s.importRows(ctx, s.ProfileID(), failed.Schema, failed.Name, r)
	if err == nil || out.Created || out.CreationUncertain || out.Committed {
		t.Fatal("bad complete source created a table", err)
	}
	var count int64
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM SYS.ALL_OBJECTS WHERE OWNER=? AND OBJECT_NAME=?`, failed.Schema, failed.Name).Scan(&count); err != nil || count != 0 {
		t.Fatal("preflight left target DDL", err, count)
	}
}

func TestNativeDamengImportNoKeyDuplicatesAndContinueOnErrorSavepoints(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "V VARCHAR(32),C CLOB,B BLOB")
	e := importEdit(map[string]contract.DataValue{"V": {Text: "same"}, "C": {Text: ""}, "B": {Binary: true, Bytes: []byte{}}})
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Edits: []contract.DataEdit{e, e, e}})
	if err != nil || !out.Committed || out.Saved.Applied != 3 {
		t.Fatal("no-key duplicate import", err)
	}
	if out.Rows[0].Outcome.Saved.Ref.TypedValues[0].Text == out.Rows[1].Outcome.Saved.Ref.TypedValues[0].Text {
		t.Fatal("duplicate rows merged physical identity")
	}
	var count int64
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)+" WHERE C IS NOT NULL AND B IS NOT NULL").Scan(&count); err != nil || count != 3 {
		t.Fatal("empty LOB/null distinction changed", err, count)
	}
	keyed := exportFixtureTable(t, s, "ID INT PRIMARY KEY,V VARCHAR(32),C CLOB,B BLOB")
	rows := []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}, "V": {Text: "one"}, "C": {Text: "long one"}, "B": {Binary: true, Bytes: []byte{0, 128, 255}}}), importEdit(map[string]contract.DataValue{"ID": {Text: "1"}, "V": {Text: "duplicate"}, "C": {Text: "must rollback"}}), importEdit(map[string]contract.DataValue{"ID": {Text: "2"}, "V": {Text: "two"}, "C": {Kind: contract.Null}})}
	out, err = s.importRows(ctx, s.ProfileID(), keyed.Schema, keyed.Name, contract.DataImportRequest{ContinueOnError: true, Edits: rows})
	if err != nil || !out.Committed || out.CommitUncertain || out.Saved.Applied != 2 || out.Rows[0].Status != "committed" || out.Rows[1].Status != "failed" || out.Rows[2].Status != "committed" {
		t.Fatal("row savepoint continuation", err, out)
	}
	var text string
	var blob []byte
	if err = s.db.QueryRowContext(ctx, "SELECT C,B FROM "+qualified(keyed)+" WHERE ID=1").Scan(&text, &blob); err != nil || text != "long one" || !bytes.Equal(blob, []byte{0, 128, 255}) {
		t.Fatal("failed duplicate disturbed earlier LOB", err)
	}
	out, err = s.importRows(ctx, s.ProfileID(), keyed.Schema, keyed.Name, contract.DataImportRequest{Mode: "replace", Edits: rows})
	if err == nil || out.Committed || out.CommitUncertain || out.Rows[0].Status != "rolled_back" {
		t.Fatal("failed replace committed deletion/prefix", err, out)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(keyed)).Scan(&count); err != nil || count != 2 {
		t.Fatal("replace failure lost original rows", err, count)
	}
}

func TestNativeDamengImportDefaultsIdentityGeneratedTriggerAndMatchedIdentity(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(32) DEFAULT 'default',N DECIMAL(38,20) DEFAULT 1.23456789012345678901,G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL")
	if _, err := s.db.ExecContext(ctx, "CREATE TRIGGER "+quote(o.Schema)+"."+quote(o.Name+"_TR")+" BEFORE INSERT OR UPDATE ON "+qualified(o)+" FOR EACH ROW BEGIN :NEW.V:=UPPER(:NEW.V); END;"); err != nil {
		t.Fatal(err)
	}
	out, err := s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Default: true}})}})
	if err != nil || !out.Committed || out.Rows[0].Outcome.GeneratedID != 9007199254740993 || out.Rows[0].Outcome.Saved.Original[1].Text != "DEFAULT" {
		t.Fatal("actual server default/identity/trigger", err, out)
	}
	out, err = s.importRows(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "update", MatchColumns: []string{"ID"}, Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "9007199254740993"}, "V": {Text: "updated"}})}})
	if err != nil || !out.Committed || out.Rows[0].Outcome.Saved.Original[1].Text != "UPDATED" {
		t.Fatal("matched identity lookup was rejected or overridden", err)
	}
	unsafe := exportFixtureTable(t, s, "ID INT PRIMARY KEY,C CLOB")
	if _, err = s.db.ExecContext(ctx, "CREATE TRIGGER "+quote(unsafe.Schema)+"."+quote(unsafe.Name+"_TR")+" BEFORE INSERT ON "+qualified(unsafe)+" FOR EACH ROW BEGIN NULL; END;"); err != nil {
		t.Fatal(err)
	}
	for _, disable := range []bool{false, true} {
		if disable {
			if _, err = s.db.ExecContext(ctx, "ALTER TRIGGER "+quote(unsafe.Schema)+"."+quote(unsafe.Name+"_TR")+" DISABLE"); err != nil {
				t.Fatal(err)
			}
		}
		out, err = s.importRows(ctx, s.ProfileID(), unsafe.Schema, unsafe.Name, contract.DataImportRequest{Edits: []contract.DataEdit{importEdit(map[string]contract.DataValue{"ID": {Text: "1"}, "C": {Text: "must not write"}})}})
		if !errors.Is(err, contract.ErrDataReadOnly) || out.Committed {
			t.Fatal("unsafe LOB trigger import accepted", err)
		}
	}
}

type importGeneratedSource struct {
	total, index int
	closed       *int
	value        func(int) contract.DataEdit
	closeErr     error
}

func (r *importGeneratedSource) Next(ctx context.Context) (contract.DataEdit, error) {
	if err := ctx.Err(); err != nil {
		return contract.DataEdit{}, err
	}
	if r.index == r.total {
		return contract.DataEdit{}, io.EOF
	}
	v := r.value(r.index)
	r.index++
	return v, nil
}
func (r *importGeneratedSource) Close() error {
	if r.closed != nil {
		*r.closed++
	}
	return r.closeErr
}

func TestNativeDamengImportStreamBoundedDiagnosticsFactoryIntegrityAndCallbacks(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY,V VARCHAR(32),B VARBINARY(8)")
	opens, closes, records := 0, 0, 0
	stream := contract.DataImportStreamRequest{Rows: 1005, ExtendedInsert: true, StatementMaxBytes: 1024, Open: func(context.Context) (contract.DataImportRowSource, error) {
		opens++
		return &importGeneratedSource{total: 1005, closed: &closes, value: func(i int) contract.DataEdit {
			return importEdit(map[string]contract.DataValue{"ID": {Text: fmt.Sprint(i)}, "V": {Text: "row"}, "B": {Binary: true, Bytes: []byte{0, 128, 255}}})
		}}, nil
	}, Record: func(row contract.DataImportRowResult) error {
		records++
		if row.Row != records || row.Status != "prepared" || row.Outcome == nil || row.Outcome.Saved != nil || row.Outcome.Previews[2].Value.Bytes[0] != 0 {
			t.Fatal("stream record identity/ownership changed")
		}
		row.Outcome.Previews[2].Value.Bytes[0] = 99
		return nil
	}}
	out, err := s.importStream(ctx, s.ProfileID(), o.Schema, o.Name, stream)
	if err != nil || !out.Committed || out.CommitUncertain || out.Progress.Processed != 1005 || out.Progress.Prepared != 1005 || len(out.Rows) != 1000 || opens != 2 || closes != 2 || records != 1005 || out.Rows[0].Outcome.Previews[2].Value.Bytes[0] != 0 {
		t.Fatal("bounded genuine source stream", err, out.Progress, opens, closes, records)
	}
	var count int64
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 1005 {
		t.Fatal("stream truncated source table", err, count)
	}
	failed := s.Namespace("SYSDBA").Object("table", "NYADB_CHANGED_IMPORT_"+nativeSuffix(t))
	t.Cleanup(func() { s.db.ExecContext(context.Background(), "DROP TABLE "+qualified(failed)) })
	opens = 0
	change := contract.DataImportStreamRequest{Rows: 1, Request: contract.DataImportRequest{NewTable: true, Columns: []contract.DataImportColumn{{Name: "ID", Type: "INT"}}}, Open: func(context.Context) (contract.DataImportRowSource, error) {
		opens++
		v := opens
		return &importGeneratedSource{total: 1, value: func(int) contract.DataEdit {
			return importEdit(map[string]contract.DataValue{"ID": {Text: fmt.Sprint(v)}})
		}}, nil
	}}
	out, err = s.importStream(ctx, s.ProfileID(), failed.Schema, failed.Name, change)
	if !errors.Is(err, connections.ErrInvalid) || !out.Created || out.Committed || out.CommitUncertain {
		t.Fatal("changed reviewed source committed or lost confirmed DDL", err, out)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(failed)).Scan(&count); err != nil || count != 0 {
		t.Fatal("changed source left DML instead of empty confirmed table", err, count)
	}
	failure := errors.New("record output rejected")
	change.Request = contract.DataImportRequest{}
	change.Record = func(contract.DataImportRowResult) error { return failure }
	opens = 0
	change.Open = func(context.Context) (contract.DataImportRowSource, error) {
		return &importGeneratedSource{total: 1, value: func(int) contract.DataEdit { return importEdit(map[string]contract.DataValue{"ID": {Text: "99"}}) }}, nil
	}
	out, err = s.importStream(ctx, s.ProfileID(), failed.Schema, failed.Name, change)
	if !errors.Is(err, failure) || out.Committed || out.CommitUncertain || out.Rows[0].Status != "rolled_back" {
		t.Fatal("callback failure bypassed original rollback", err, out)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(failed)).Scan(&count); err != nil || count != 0 {
		t.Fatal("callback failure committed prefix", err, count)
	}
}

func TestNativeDamengImportWholeRowLOBFramesAndFullByteLimits(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	var fields []string
	values := map[string]contract.DataValue{}
	text := strings.Repeat("\n", contract.DataEditMaxBytes)
	for i := range 16 {
		name := fmt.Sprintf("C%d", i)
		fields = append(fields, name+" CLOB")
		values[name] = contract.DataValue{Text: text}
	}
	o := exportFixtureTable(t, s, strings.Join(fields, ","))
	stream := contract.DataImportStreamRequest{Rows: 1, Open: func(context.Context) (contract.DataImportRowSource, error) {
		return &importGeneratedSource{total: 1, value: func(int) contract.DataEdit { return importEdit(values) }}, nil
	}}
	out, err := s.importStream(ctx, s.ProfileID(), o.Schema, o.Name, stream)
	if err != nil || !out.Committed || out.Progress.Prepared != 1 {
		t.Fatal("exact escaped 16 MiB row rejected by worker frames", err, out)
	}
	var size int64
	if err = s.db.QueryRowContext(ctx, "SELECT LENGTHB(C0)+LENGTHB(C15) FROM "+qualified(o)).Scan(&size); err != nil || size != 2*contract.DataEditMaxBytes {
		t.Fatal("split native LOB payload truncated", err, size)
	}
	values["C0"] = contract.DataValue{Text: text + "x"}
	out, err = s.importStream(ctx, s.ProfileID(), o.Schema, o.Name, stream)
	if !errors.Is(err, contract.ErrDataEditLimit) || out.Committed || out.Progress.Processed != 0 {
		t.Fatal("oversized source bypassed complete preflight", err, out)
	}
	var count int64
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 1 {
		t.Fatal("oversize source changed existing rows", err, count)
	}
}

var _ contract.DataImportRowSource = (*importGeneratedSource)(nil)

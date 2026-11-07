package dameng

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqlexport"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func TestNativeDamengSQLIdentityAndCallerTransaction(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID BIGINT IDENTITY(9007199254740993,1) PRIMARY KEY,V VARCHAR(128) DEFAULT 'native default',G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL")
	structure, err := s.TableMetadata(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	table, err := sqlexport.NewDamengTable(o, structure.Columns)
	if err != nil {
		t.Fatal(err)
	}
	values := []contract.DataValue{{Kind: contract.Integer, Text: "9007199254740994"}, {Kind: contract.Text, Text: "literal ' & \x00\r\n"}, {Kind: contract.Integer, Text: "9007199254740995"}}
	statement, err := table.Insert(values)
	if err != nil || strings.Contains(statement, "COMMIT") {
		t.Fatal(err)
	}
	parts, err := sqltext.SplitDameng(statement)
	if err != nil || len(parts) != 1 {
		t.Fatal("identity block fragmented", err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, parts[0]); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)).Scan(&count); err != nil || count != 0 {
		t.Fatal("exported identity SQL committed the caller transaction", err, count)
	}
	if _, err = conn.ExecContext(ctx, parts[0]); err != nil {
		t.Fatal(err)
	}
	var id, value, derived string
	if err = conn.QueryRowContext(ctx, "SELECT ID,V,G FROM "+qualified(o)+" WHERE ID=9007199254740994").Scan(&id, &value, &derived); err != nil || id != values[0].Text || value != values[1].Text || derived != values[2].Text {
		t.Fatal("native identity or scalar control characters changed", err)
	}
	if _, err = conn.ExecContext(ctx, parts[0]); err == nil {
		t.Fatal("duplicate identity export unexpectedly succeeded")
	}
	// OFF must also be restored after a failing INSERT. Explicit ID would be
	// accepted while ON; this independent statement must be rejected.
	if _, err = conn.ExecContext(ctx, "INSERT INTO "+qualified(o)+"(ID,V) VALUES(9007199254740996,'off required')"); err == nil {
		t.Fatal("failed identity block left IDENTITY_INSERT enabled")
	}
	values[0], values[1], values[2] = contract.DataValue{Default: true}, contract.DataValue{Default: true}, contract.DataValue{}
	statement, err = table.Insert(values)
	if err != nil || strings.Contains(statement, "IDENTITY_INSERT") {
		t.Fatal("identity DEFAULT was not omitted", err)
	}
	parts, err = sqltext.SplitDameng(statement)
	if err != nil || len(parts) != 1 {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, parts[0]); err != nil {
		t.Fatal("server identity/default SQL", err)
	}
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qualified(o)+" WHERE V='native default'").Scan(&count); err != nil || count != 1 {
		t.Fatal("native default lost", err)
	}
}

func TestNativeDamengSQLTemporalIndependentOfSessionFormats(t *testing.T) {
	cfg, password := nativeFixture(t)
	s := fixtureSession(t, cfg, password)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	o := exportFixtureTable(t, s, "ID INT PRIMARY KEY,D DATE,T TIME(6),TZ TIME(6) WITH TIME ZONE,TS TIMESTAMP(6),TSZ TIMESTAMP(6) WITH TIME ZONE,T0 TIME(0),TS0 TIMESTAMP(0)")
	input := map[string]contract.DataValue{
		"ID":  {Kind: contract.Integer, Text: "1"},
		"D":   {Kind: contract.DateTime, Text: "2026-11-07"},
		"T":   {Kind: contract.DateTime, Text: "12:34:56.123456"},
		"TZ":  {Kind: contract.DateTime, Text: "12:34:56.123456 -05:30"},
		"TS":  {Kind: contract.DateTime, Text: "2026-11-07 12:34:56.123456"},
		"TSZ": {Kind: contract.DateTime, Text: "2026-11-07 12:34:56.123456 +08:00"},
		"T0":  {Kind: contract.DateTime, Text: "23:59:58"},
		"TS0": {Kind: contract.DateTime, Text: "2026-11-07 23:59:58"},
	}
	runtime := contract.ComposeData(s)
	if result, err := runtime.Importer(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataImportRequest{Mode: "append", Edits: []contract.DataEdit{{Kind: "insert", Values: input}}}); err != nil || result == nil || !result.Committed {
		t.Fatal("independent bound temporal seed", err)
	}
	structure, err := s.TableMetadata(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	fields := make([]string, len(structure.Columns))
	for i, column := range structure.Columns {
		fields[i] = column.Name
	}
	read := func() []contract.DataValue {
		t.Helper()
		var values []contract.DataValue
		rows := 0
		if err := runtime.Exporter(ctx, s.ProfileID(), o.Schema, o.Name, contract.DataOptions{Object: o}, fields, func(row []contract.DataValue) error {
			rows++
			values = append([]contract.DataValue(nil), row...)
			return nil
		}); err != nil || rows != 1 {
			t.Fatal("independent temporal read", err, rows)
		}
		return values
	}
	before := read()
	table, err := sqlexport.NewDamengTable(o, structure.Columns)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := table.Insert(before)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := sqltext.SplitDameng(statement)
	if err != nil || len(parts) != 1 {
		t.Fatal("temporal SQL fragmented", err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, setting := range []string{
		"NLS_DATE_FORMAT='DD/MM/YYYY'",
		"NLS_TIME_FORMAT='HH24.MI.SS.FF6'",
		"NLS_TIME_TZ_FORMAT='HH24.MI.SS.FF6 TZH:TZM'",
		"NLS_TIMESTAMP_FORMAT='DD/MM/YYYY HH24.MI.SS.FF6'",
		"NLS_TIMESTAMP_TZ_FORMAT='DD/MM/YYYY HH24.MI.SS.FF6 TZH:TZM'",
	} {
		if _, err = conn.ExecContext(ctx, "ALTER SESSION SET "+setting); err != nil {
			t.Fatal("alter captured native session format", setting, err)
		}
	}
	if _, err = conn.ExecContext(ctx, "SELECT CAST('2026-11-07' AS DATE) FROM DUAL"); err == nil {
		t.Fatal("native NLS change did not reject an implicit ISO string CAST")
	}
	if _, err = conn.ExecContext(ctx, "DELETE FROM "+qualified(o)); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, parts[0]); err != nil {
		t.Fatal("native SQL depends on target NLS formats", err)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("native date/time/timezone/fraction changed under altered NLS formats", before, after)
	}
}

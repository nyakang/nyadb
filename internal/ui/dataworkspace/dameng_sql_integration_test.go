package dataworkspace

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/dbruntime"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

func TestDamengNativeSQLCopyAndExportRoundTrips(t *testing.T) {
	p, password := damengUIFixture(t)
	p.ID = 1211
	m := dbruntime.New()
	defer m.Close()
	dbruntime.RegisterBuiltins(m, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := m.Connect(ctx, p, password); err != nil {
		t.Fatal(err)
	}
	session, err := m.Session(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := contract.Require[contract.QueryProvider](session)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := provider.ReserveQuery(98)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	schema := fmt.Sprintf("SQL 源_%d", time.Now().UnixNano())
	name := `Rows " data`
	target := quote(schema) + "." + quote(name)
	if _, err = lease.Execute(ctx, "SYSDBA", "CREATE SCHEMA "+quote(schema)+" AUTHORIZATION SYSDBA; CREATE TABLE "+target+" (ID BIGINT PRIMARY KEY,N DECIMAL(38,20),F DOUBLE,Q BIT,T VARCHAR(128),V VARBINARY(128),C CLOB,B BLOB,D DATE,TM TIME(6),TS TIMESTAMP(6),TZ TIMESTAMP(6) WITH TIME ZONE,Z VARCHAR(128),E VARBINARY(128),DV VARCHAR(64) DEFAULT 'server default',G BIGINT GENERATED ALWAYS AS(ID+1) VIRTUAL)"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := lease.Execute(cleanup, "SYSDBA", "DROP SCHEMA "+quote(schema)+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	runtime := m.Data(p.ID)
	makeRow := func(id, text string) contract.DataEdit {
		return contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{
			"ID": {Kind: contract.Integer, Text: id}, "N": {Kind: contract.Decimal, Text: "9007199254740993.12345678901234567890"},
			"F": {Kind: contract.Floating, Text: "1.2345678901234567"}, "Q": {Kind: contract.Boolean, Text: "true"}, "T": {Kind: contract.Text, Text: text},
			"V": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 128, 255, 39}}, "C": {Kind: contract.Text, Text: "中文🙂'\r\n/\n\x00&\\"},
			"B": {Kind: contract.Binary, Binary: true, Bytes: []byte{0, 128, 255, 10, 39}}, "D": {Kind: contract.DateTime, Text: "2026-11-07"},
			"TM": {Kind: contract.DateTime, Text: "12:34:56.123456"}, "TS": {Kind: contract.DateTime, Text: "2026-11-07 12:34:56.123456"},
			"TZ": {Kind: contract.DateTime, Text: "2026-11-07 12:34:56.123456 +08:00"}, "Z": {Null: true}, "E": {Kind: contract.Binary, Binary: true, Bytes: []byte{}},
		}}
	}
	seed := []contract.DataEdit{makeRow("9007199254740992", "neighbor"), makeRow("9007199254740993", "target")}
	if result, err := runtime.Importer(ctx, p.ID, schema, name, contract.DataImportRequest{Mode: "append", Edits: seed}); err != nil || result == nil || !result.Committed {
		t.Fatal("independent native bound seed", err)
	}
	state := Session{ProfileID: p.ID, Generation: session.Generation(), Connected: true}
	s := New(Dependencies{Session: func() Session { return state }, Runtime: func() Runtime { return runtime }})
	d := s.OpenObject(1, m.Namespace(p.ID, schema).Object("table", name), session.Capabilities())
	defer s.Close(d)
	var services ui.Services
	tt := ui.NewTester(func(c *ui.Context) {
		services = c.Services()
		s.Consume(services)
		theme.Apply(c)
		s.View(c, d)
		s.Modals(c)
	}, 1500, 900)
	capture := func(name string) {
		t.Helper()
		if root := os.Getenv("NYADB_DAMENG_VISUAL_DIR"); root != "" {
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			if err = png.Encode(file, tt.Image()); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	wait := func(done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(45 * time.Second); !done() && time.Now().Before(deadline); {
			tt.Frame()
			time.Sleep(2 * time.Millisecond)
		}
		if !done() {
			t.Fatal("native SQL workflow did not settle", d.err, d.transfer.Err)
		}
		tt.Frame()
	}
	s.Load(services, d, false)
	wait(func() bool { return !d.loading })
	if d.page == nil || d.err != "" || len(d.page.Rows) != 2 {
		t.Fatal("native SQL source grid", d.err)
	}
	if err = tt.RightClick("target"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("复制为", "Insert 语句"); err != nil {
		t.Fatal(err)
	}
	insert := tt.Clipboard()
	if !strings.Contains(insert, target) || strings.Contains(insert, "_utf8mb4") || strings.Contains(insert, `"G"`) {
		t.Fatal("native menu did not copy qualified INSERT")
	}
	s.EditCell(services, d, 1, "N", contract.Cell{Kind: contract.Decimal, Text: "9007199254740993.98765432109876543210"})
	tt.Frame()
	if err = tt.RightClick("target"); err != nil {
		t.Fatal(err)
	}
	if err = tt.ChooseMenuItem("复制为", "Update 语句"); err != nil {
		t.Fatal(err)
	}
	update := tt.Clipboard()
	if result, err := lease.Execute(ctx, "SYSDBA", update); err != nil || result.AffectedRows != 1 {
		t.Fatal("native exact UPDATE copy", err, result.AffectedRows)
	}
	result, err := lease.Execute(ctx, "SYSDBA", "SELECT ID,N,T FROM "+target+" ORDER BY ID")
	sameDecimal := func(a, b string) bool {
		x, ok := new(big.Rat).SetString(a)
		y, valid := new(big.Rat).SetString(b)
		return ok && valid && x.Cmp(y) == 0
	}
	if err != nil || len(result.Rows) != 2 || result.Rows[0][0] != "9007199254740992" || result.Rows[1][0] != "9007199254740993" || result.Rows[0][2] != "neighbor" || !sameDecimal(result.Rows[0][1], "9007199254740993.12345678901234567890") || !sameDecimal(result.Rows[1][1], "9007199254740993.98765432109876543210") {
		t.Fatal("UPDATE rounded its key or value", err, result.Rows)
	}
	if _, err = lease.Execute(ctx, "SYSDBA", "DELETE FROM "+target+" WHERE ID=9007199254740993; "+insert); err != nil {
		t.Fatal("native INSERT copy roundtrip", err)
	}
	s.discardMySQLData(d)
	s.Load(services, d, false)
	wait(func() bool { return !d.loading })
	read := func() [][]contract.DataValue {
		t.Helper()
		var rows [][]contract.DataValue
		fields := append([]string(nil), d.page.Columns...)
		if err := runtime.Exporter(ctx, p.ID, schema, name, contract.DataOptions{Object: d.object, SortColumn: "ID"}, fields, func(row []contract.DataValue) error {
			owned := make([]contract.DataValue, len(row))
			for i, value := range row {
				value.Bytes = bytes.Clone(value.Bytes)
				owned[i] = value
			}
			rows = append(rows, owned)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := read()
	if err = tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("CSV"); err != nil {
		t.Fatal(err)
	}
	if err = tt.Click("SQL"); err != nil {
		t.Fatal(err)
	}
	if d.transfer.Format != "SQL" {
		t.Fatal("native SQL format missing from wizard")
	}
	capture("dameng-live-sql-format.png")
	if err = tt.Click("下一步"); err != nil {
		t.Fatal(err)
	}
	d.transfer.Scope = "all"
	if err = tt.Click("G"); err != nil {
		t.Fatal("computed column exclusion", err)
	}
	for range 2 {
		if err = tt.Click("下一步"); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "native.sql")
	if err = tt.Click("目标文件路径"); err != nil {
		t.Fatal(err)
	}
	tt.Type(path)
	if err = tt.Click("导出"); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return !d.transfer.Busy })
	if !d.transfer.Completed || d.transfer.Err != "" {
		t.Fatal("native complete SQL export", d.transfer.Err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), target) || strings.Contains(string(data), "_utf8mb4") || strings.Contains(string(data), `"G"`) || strings.ContainsRune(string(data), 0) {
		t.Fatal("native file contains invalid SQL", err)
	}
	capture("dameng-live-sql-export.png")
	if _, err = lease.Execute(ctx, "SYSDBA", "DELETE FROM "+target); err != nil {
		t.Fatal(err)
	}
	files, ok := lease.(contract.QueryFileLease)
	if !ok {
		t.Fatal("native SQL file lease absent")
	}
	if _, err = files.ExecuteFile(ctx, "SYSDBA", bytes.NewReader(data)); err != nil {
		t.Fatal("native SQL file roundtrip", err)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("SQL export/replay changed exact types, NULL/empty, Unicode, binary, dates or defaults")
	}
}

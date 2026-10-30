package datasources_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"github.com/nyakang/nyadb/internal/services/datasources"
	"io"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeODBCIntegration(t *testing.T) {
	if os.Getenv("NYADB_DATASOURCES_ODBC_IT") != "1" {
		t.Skip("requires explicitly enabled disposable native SQLite ODBC fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p := filepath.Join(t.TempDir(), "fixture_数据.sqlite")
	db, e := sql.Open("sqlite", p)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE "source_表" (id TEXT,note TEXT,payload BLOB,amount TEXT);INSERT INTO "source_表" VALUES ('9007199254740993','中文'||char(13)||char(10),X'00FF80','9007199254740993.1234567890'),('2','',X'',NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	cfg := datasources.Config{Kind: datasources.ODBC, ConnectionString: "DRIVER={SQLite3};Database=" + strings.ReplaceAll(p, "}", "}}") + ";ReadOnly=1;"}
	names, e := datasources.Drivers(ctx, cfg)
	if e != nil || len(names) == 0 {
		t.Fatal(names, e)
	}
	s, e := datasources.Open(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	tables, e := s.Tables(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var target datasources.Table
	for _, x := range tables {
		if x.Name == "source_表" {
			target = x
		}
	}
	if target.Name == "" {
		t.Fatal(tables)
	}
	t.Logf("target=%+v", target)
	cols, e := s.Columns(ctx, target)
	if e != nil || len(cols) != 4 {
		t.Fatal(cols, e)
	}
	r, e := s.Rows(ctx, target)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	row, e := r.Next(ctx)
	if e != nil || row[0].Text != "9007199254740993" || row[1].Text != "中文\r\n" || hex.EncodeToString(row[2].Bytes) != "00ff80" || row[3].Text != "9007199254740993.1234567890" {
		t.Fatal(row, e)
	}
	row, e = r.Next(ctx)
	if e != nil || row[1].Null || row[1].Text != "" || !row[2].Binary || row[2].Bytes == nil || len(row[2].Bytes) != 0 || !row[3].Null {
		t.Fatal(row, e)
	}
	if _, e = r.Next(ctx); e != io.EOF {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = db.QueryRow(`SELECT COUNT(*) FROM "source_表"`).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
func TestNativeParadoxIntegration(t *testing.T) {
	p := os.Getenv("NYADB_DATASOURCES_PARADOX_FILE")
	if p == "" {
		t.Skip("requires explicitly supplied self-created Paradox fixture")
	}
	ctx := context.Background()
	before, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	s, e := datasources.Open(ctx, datasources.Config{Kind: datasources.Paradox, Path: p})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	tables, e := s.Tables(ctx)
	if e != nil || len(tables) != 1 {
		t.Fatal(tables, e)
	}
	r, e := s.Rows(ctx, tables[0])
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	row, e := r.Next(ctx)
	if e != nil || len(row) != 5 || row[0].Text != "1" || row[1].Text != "alpha" || row[2].Text != "12.5" || row[3].Text != "2026-10-30" || hex.EncodeToString(row[4].Bytes) != "01ff03" {
		t.Fatal(row, e)
	}
	row, e = r.Next(ctx)
	if e != nil || row[0].Text != "2" || !row[1].Null || !row[2].Null || !row[3].Null {
		t.Fatal(row, e)
	}
	if _, e = r.Next(ctx); e != io.EOF {
		t.Fatal(e)
	}
	s.Close()
	after, e := os.ReadFile(p)
	if e != nil || string(before) != string(after) {
		t.Fatal("source changed", e)
	}
}
func TestNativeAccessIntegration(t *testing.T) {
	p := os.Getenv("NYADB_DATASOURCES_ACCESS_FILE")
	if p == "" {
		t.Skip("requires explicitly supplied disposable/public Access fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, e := datasources.Open(ctx, datasources.Config{Kind: datasources.Access, Path: p})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	tables, e := s.Tables(ctx)
	if e != nil || len(tables) == 0 {
		t.Fatal(tables, e)
	}
	for _, table := range tables {
		cols, e := s.Columns(ctx, table)
		if e != nil || len(cols) == 0 {
			t.Fatal(table, cols, e)
		}
		r, e := s.Rows(ctx, table)
		if e != nil {
			t.Fatal(table, e)
		}
		count := 0
		for {
			row, e := r.Next(ctx)
			if e == io.EOF {
				break
			}
			if e != nil {
				r.Close()
				t.Fatal(table, e)
			}
			if len(row) != len(cols) {
				t.Fatal(table, row, cols)
			}
			count++
		}
		r.Close()
		t.Logf("table=%s fields=%d rows=%d", table.Name, len(cols), count)
	}
}

package datasources_test

import (
	"bytes"
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

// The supplied self-created fixture contains inline and sidecar-backed memo
// and binary fields. Its zero-length blob records use libpx's NULL encoding.
func TestNativeParadoxMemoIntegration(t *testing.T) {
	path := os.Getenv("NYADB_DATASOURCES_PARADOX_MEMO_FILE")
	if path == "" {
		t.Skip("requires an explicitly supplied self-created memo/blob Paradox fixture")
	}
	ctx := context.Background()
	sidecar := strings.TrimSuffix(path, filepath.Ext(path)) + ".mb"
	beforeDB, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeMB, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	source, err := datasources.Open(ctx, datasources.Config{Kind: datasources.Paradox, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	tables, err := source.Tables(ctx)
	if err != nil || len(tables) != 1 {
		t.Fatal(tables, err)
	}
	reader, err := source.Rows(ctx, tables[0])
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for i, size := range []int{5, 2000, 65536, 0, 0} {
		row, err := reader.Next(ctx)
		if err != nil || len(row) != 3 {
			t.Fatal(i, row, err)
		}
		if size == 0 {
			if !row[1].Null || !row[2].Null {
				t.Fatal("libpx NULL blob was changed", row)
			}
			continue
		}
		wantText, wantBytes := make([]byte, size), make([]byte, size)
		for j := range wantText {
			wantText[j], wantBytes[j] = byte('A'+j%26), byte(j%256)
		}
		if row[1].Text != string(wantText) || row[1].Null || row[1].Binary || !row[2].Binary || !bytes.Equal(row[2].Bytes, wantBytes) {
			t.Fatalf("inline/sidecar field of %d bytes changed", size)
		}
	}
	if _, err = reader.Next(ctx); err != io.EOF {
		t.Fatal(err)
	}
	if err = source.Close(); err != nil {
		t.Fatal(err)
	}
	afterDB, errDB := os.ReadFile(path)
	afterMB, errMB := os.ReadFile(sidecar)
	if errDB != nil || errMB != nil || !bytes.Equal(beforeDB, afterDB) || !bytes.Equal(beforeMB, afterMB) {
		t.Fatal("memo source or sidecar changed", errDB, errMB)
	}
	for _, test := range []struct {
		name string
		memo []byte
	}{{"missing", nil}, {"truncated", beforeMB[:min(512, len(beforeMB))]}} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			broken := filepath.Join(directory, "memo.db")
			if err := os.WriteFile(broken, beforeDB, 0600); err != nil {
				t.Fatal(err)
			}
			if test.memo != nil {
				if err := os.WriteFile(filepath.Join(directory, "memo.mb"), test.memo, 0600); err != nil {
					t.Fatal(err)
				}
			}
			opened, err := datasources.Open(ctx, datasources.Config{Kind: datasources.Paradox, Path: broken})
			if err != nil {
				return
			}
			defer opened.Close()
			tables, err := opened.Tables(ctx)
			if err != nil {
				return
			}
			rows, err := opened.Rows(ctx, tables[0])
			if err != nil {
				return
			}
			defer rows.Close()
			for i := 0; i < 2; i++ {
				if _, err := rows.Next(ctx); err != nil {
					return
				}
			}
			t.Fatal("missing or truncated sidecar produced a complete external blob")
		})
	}
}

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

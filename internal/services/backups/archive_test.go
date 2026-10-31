package backups

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupTypedRoundTripAndChecksum(t *testing.T) {
	ctx := context.Background()
	var b bytes.Buffer
	w := NewWriter(&b, "sandbox", "8.4")
	row := []contract.Cell{{Kind: contract.Null}, {Kind: contract.Text, Text: "NULL"}, {Kind: contract.Text}, {Kind: contract.Binary}, {Kind: contract.Binary, Bytes: []byte{0, 255}}, {Kind: contract.Decimal, Text: "9007199254740993.0001"}}
	columns := make([]contract.Column, len(row))
	for i := range columns {
		columns[i].Name = string(rune('a' + i))
	}
	if e := w.Add(ctx, Object{Kind: "TABLE", Name: "typed", Columns: columns}, "CREATE TABLE typed (a text)", func(emit func([]contract.Cell) error) error { return emit(row) }); e != nil {
		t.Fatal(e)
	}
	if _, e := w.Finish(ctx); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "backup.zip")
	if e := os.WriteFile(path, b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	rows, e := r.Rows(0)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	got, e := rows.Next(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if got[0].Kind != contract.Null || got[1].Text != "NULL" || got[2].Text != "" || got[3].Kind != contract.Binary || got[4].Display() != "0x00ff" || got[5].Text != row[5].Text {
		t.Fatal(got)
	}
	if _, e = rows.Next(ctx); e != io.EOF {
		t.Fatal(e)
	}
	// Rewrite a valid ZIP with changed rows and the original signed checksum.
	z, e := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if e != nil {
		t.Fatal(e)
	}
	var tampered bytes.Buffer
	out := zip.NewWriter(&tampered)
	for _, entry := range z.File {
		src, e := entry.Open()
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(src)
		src.Close()
		if entry.Name == "objects/00000.jsonl" {
			raw = bytes.ReplaceAll(raw, []byte("9007199254740993.0001"), []byte("9007199254740993.0002"))
		}
		dst, _ := out.Create(entry.Name)
		_, _ = dst.Write(raw)
	}
	_ = out.Close()
	_ = os.WriteFile(path, tampered.Bytes(), 0600)
	if _, e = Open(ctx, path); e == nil {
		t.Fatal("tampered backup accepted")
	}
}
func TestBackupIncompleteTruncatedAndCancelledAreRejected(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b, "sandbox", "8.4")
	if e := w.Add(context.Background(), Object{Kind: "TABLE", Name: "t", Columns: []contract.Column{{Name: "a"}}}, "CREATE TABLE t (a text)", func(emit func([]contract.Cell) error) error {
		return emit([]contract.Cell{{Kind: contract.Text, Truncated: true}})
	}); e == nil {
		t.Fatal("truncated data accepted")
	}
	if _, e := w.Finish(context.Background()); e == nil {
		t.Fatal("failed archive completed")
	}
	var incomplete bytes.Buffer
	z := zip.NewWriter(&incomplete)
	f, _ := z.Create("manifest.json")
	_ = json.NewEncoder(f).Encode(Manifest{Format: "nyadb-backup", Version: Version, Database: "sandbox"})
	_ = z.Close()
	path := filepath.Join(t.TempDir(), "bad.zip")
	_ = os.WriteFile(path, incomplete.Bytes(), 0600)
	if _, e := Open(context.Background(), path); e == nil {
		t.Fatal("incomplete archive accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = NewWriter(io.Discard, "sandbox", "8.4")
	if _, e := w.Finish(ctx); e == nil {
		t.Fatal("cancelled archive completed")
	}
}

func TestBackupRefusesEncodedExpansionBeforeCompletingArchive(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b, "db", "8.4")
	cell := contract.Cell{Kind: contract.Text, Text: strings.Repeat("\x00", 6<<20)}
	if e := w.Add(context.Background(), Object{Kind: "TABLE", Name: "t", Columns: []contract.Column{{Name: "body"}}}, "CREATE TABLE t (body longtext)", func(emit func([]contract.Cell) error) error { return emit([]contract.Cell{cell}) }); e == nil {
		t.Fatal("unreadable encoded row accepted")
	}
	if _, e := w.Finish(context.Background()); e == nil {
		t.Fatal("invalid row published as complete")
	}
}

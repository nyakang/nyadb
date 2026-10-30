package datasources

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingNativeComponentsReturnSafeConfigurationDetails(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing-library")
	c := Config{Kind: ODBC, LibraryPath: missing, ConnectionString: "UID=private-user;PWD=must-not-leak;"}
	caps := Capabilities(ctx, c)
	if len(caps) != 3 || caps[0].Available || caps[0].Detail == "" {
		t.Fatal(caps)
	}
	_, e := Open(ctx, c)
	if !errors.Is(e, ErrUnavailable) || strings.Contains(e.Error(), "must-not-leak") || strings.Contains(e.Error(), missing) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = Open(ctx, c); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestMDBRowsPreserveExactValuesMissingNullAndBinary(t *testing.T) {
	cols := []Column{{Name: "id"}, {Name: "note"}, {Name: "bytes"}, {Name: "absent"}}
	r, e := decodeMDBRow([]byte(`{"id":9007199254740993,"note":"中文\r\n","bytes":{"$binary":"AP+A","$type":"00"}}`), cols)
	if e != nil || r[0].Text != "9007199254740993" || r[1].Text != "中文\r\n" || !r[2].Binary || len(r[2].Bytes) != 3 || !r[3].Null {
		t.Fatal(r, e)
	}
	r, e = decodeMDBRow([]byte(`{"id":1,"note":"","bytes":{"$binary":"","$type":"00"},"absent":null}`), cols)
	if e != nil || r[1].Null || r[1].Text != "" || !r[2].Binary || r[2].Bytes == nil || len(r[2].Bytes) != 0 || !r[3].Null {
		t.Fatal(r, e)
	}
	for _, line := range []string{`{"unexpected":1}`, `{"id":[1]}`, `{"bytes":{"$binary":"not base64","$type":"00"}}`, `{"id":{"secret":"do not print"}}`} {
		_, e = decodeMDBRow([]byte(line), cols)
		if e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatal(e)
		}
	}
	_, e = decodeMDBRow([]byte(`{"note":"`+strings.Repeat("x", 199999)+`"}`), cols)
	if !errors.Is(e, ErrValueLimit) {
		t.Fatal(e)
	}
}
func TestMDBColumnsAndFileValidationDoNotExposePaths(t *testing.T) {
	if e := validateFile(filepath.Join(t.TempDir(), "private-password.accdb")); e == nil || strings.Contains(e.Error(), "private-password") {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "db.mdb")
	if e := os.WriteFile(p, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := validateFile(p); e != nil {
		t.Fatal(e)
	}
	if e := checkedColumns([]Column{{Name: "duplicate"}, {Name: "duplicate"}}); e == nil {
		t.Fatal("duplicate field identity accepted")
	}
}

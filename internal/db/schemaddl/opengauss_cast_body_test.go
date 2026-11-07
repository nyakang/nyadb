package schemaddl

import (
	"strings"
	"testing"
)

func TestOpenGaussCastBodySignaturePreservesBindingsAndValues(t *testing.T) {
	original := `select pg_catalog.interval_in(pg_catalog.bpcharout($1), 0::Oid, $2)`
	want, err := OpenGaussCastBodySignature(original)
	if err != nil {
		t.Fatal(err)
	}
	formatted := " SELECT\n PG_CATALOG.INTERVAL_IN ( pg_catalog.BPCHAROUT ($1), 0 :: OID, $2 ) "
	if got, err := OpenGaussCastBodySignature(formatted); err != nil || got != want {
		t.Fatal("harmless formatting changed native signature", err)
	}
	for _, body := range []string{
		strings.Replace(original, "$2", "$1", 1),
		strings.Replace(original, "$1", "$10", 1),
		strings.Replace(original, "pg_catalog.bpcharout", "public.bpcharout", 1),
		strings.Replace(original, "pg_catalog.bpcharout", "bpcharout", 1),
		strings.Replace(original, "bpcharout", `"bpcharout"`, 1),
		strings.Replace(original, "0::Oid", "1::Oid", 1),
		strings.Replace(original, "$2", "pg_catalog.nextval('effects')", 1),
		original + " UNION SELECT NULL",
	} {
		if got, err := OpenGaussCastBodySignature(body); err == nil && got == want {
			t.Errorf("changed binding matched original: %s", body)
		}
	}
	for _, pair := range [][2]string{
		{`select 'A'`, `select 'a'`},
		{`select 'A'`, `select E'A'`},
		{`select E'A\n'`, `select E'A\\n'`},
		{`select $$A$$`, `select $$a$$`},
		{`select "Function"($1)`, `select "function"($1)`},
	} {
		a, ea := OpenGaussCastBodySignature(pair[0])
		b, eb := OpenGaussCastBodySignature(pair[1])
		if ea != nil || eb != nil || a == b {
			t.Errorf("literal or quoted identifier changed: %q %q (%v, %v)", pair[0], pair[1], ea, eb)
		}
	}
}

func TestOpenGaussCastBodySignatureRejectsMalformedOrMultipleStatements(t *testing.T) {
	for _, body := range []string{"", "UPDATE pg_proc SET prosrc='x'", "select $1; select $2", "select ($1", "select $1)", "select 'unterminated", "select $1 -- hidden", "select /* hidden */ $1", "select " + strings.Repeat("x", 64<<10)} {
		if _, err := OpenGaussCastBodySignature(body); err == nil {
			t.Errorf("unsupported body accepted: %.80s", body)
		}
	}
}

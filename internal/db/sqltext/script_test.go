package sqltext

import (
	"io"
	"strings"
	"testing"
)

func TestScriptScannerProtectsQuotesCommentsAndDelimiterBodies(t *testing.T) {
	source := "-- prelude;\nSELECT 'semi;colon', `a;b`, 'it''s;';\n/* comment; */\nDELIMITER $$\nCREATE PROCEDURE `p`() BEGIN SELECT 'a$$b'; SELECT 2; END$$\nDELIMITER ;\nSELECT 3; # trailing comment;\n"
	s := NewScriptScanner(strings.NewReader(source))
	var statements []string
	for {
		sql, e := s.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		statements = append(statements, sql)
	}
	if len(statements) != 3 || !strings.Contains(statements[1], "SELECT 2; END") || strings.Contains(strings.Join(statements, ""), "DELIMITER") || statements[2] != "SELECT 3" {
		t.Fatalf("incorrect statements: %#v", statements)
	}
}
func TestScriptScannerRejectsMalformedAndOversizedStatements(t *testing.T) {
	for _, sql := range []string{"SELECT 'unterminated", "SELECT 1 /* open", "DELIMITER\nSELECT 1", "DELIMITER 'bad'\n"} {
		s := NewScriptScanner(strings.NewReader(sql))
		if _, e := s.Next(); e == nil {
			t.Fatalf("invalid script accepted: %q", sql)
		}
	}
	s := NewScriptScanner(strings.NewReader("SELECT " + strings.Repeat("x", 32)))
	s.MaxStatementBytes = 16
	if _, e := s.Next(); e == nil {
		t.Fatal("statement limit ignored")
	}
	s = NewScriptScanner(strings.NewReader("SELECT 'a\\'; SELECT 2;"))
	s.BackslashEscapes = false
	first, e := s.Next()
	second, e2 := s.Next()
	if e != nil || e2 != nil || first != "SELECT 'a\\'" || second != "SELECT 2" {
		t.Fatal("NO_BACKSLASH_ESCAPES mode not respected")
	}
}

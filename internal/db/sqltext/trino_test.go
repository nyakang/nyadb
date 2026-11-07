package sqltext

import (
	"strings"
	"testing"
)

func TestTrinoSQLQuotesCommentsRoutinesAndFormat(t *testing.T) {
	source := "select 'ordinary\\';--no whitespace; select never\nselect 'It''s native' as \"Mixed\"; select X'00ff'"
	parts, err := SplitTrino(source)
	if err != nil || len(parts) != 3 {
		t.Fatal("native splitting failed", parts, err)
	}
	if TrinoStatementAt(source, strings.Index(source, "It''s")) != parts[1] {
		t.Fatal("caret chose wrong statement")
	}
	formatted, err := FormatTrino(source)
	if err != nil || !strings.Contains(formatted, "'ordinary\\'") || !strings.Contains(formatted, "--no whitespace; select never\n") || !strings.Contains(formatted, "'It''s native'") || !strings.Contains(formatted, `"Mixed"`) {
		t.Fatal("format changed native protected text", formatted, err)
	}
	parts, err = SplitTrino("WITH FUNCTION twice(x BIGINT) RETURNS BIGINT BEGIN RETURN x*2; END SELECT twice(3);")
	if err != nil || len(parts) != 1 {
		t.Fatal("native routine split into commands", err)
	}
	for _, s := range []string{"SELECT `mysql`", "SELECT $$postgres$$", "SELECT 1 # mysql", "SELECT 'unclosed", "/* outer /* nested */ */ SELECT 1", "SELECT 1\x00"} {
		if _, err := SplitTrino(s); err == nil {
			t.Fatalf("non-native SQL admitted: %s", s)
		}
	}
}

func TestTrinoCommentsDoNotBecomeExecutableStatements(t *testing.T) {
	for _, source := range []string{"SELECT 1; -- trailing comment", "/* before */; SELECT 1; /* after */; -- last", "SELECT 1; /* ; SELECT 2 */"} {
		parts, err := SplitTrino(source)
		if err != nil || len(parts) != 1 || !strings.Contains(parts[0], "SELECT 1") {
			t.Fatal("comments became a second command", parts, err)
		}
		if err = SingleTrinoStatement(source); err != nil {
			t.Fatal("one real statement rejected", err)
		}
	}
	for _, source := range []string{"-- comment", "/* comment */; -- last"} {
		parts, err := SplitTrino(source)
		if err == nil || len(parts) != 0 || SingleTrinoStatement(source) == nil {
			t.Fatal("comment-only input became SQL", parts, err)
		}
	}
}

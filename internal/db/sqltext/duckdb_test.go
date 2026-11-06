package sqltext

import (
	"strings"
	"testing"
)

func TestDuckDBStringsBatchesAndCaret(t *testing.T) {
	source := `/* outer /* inner */ */ SELECT 'a\'; SELECT $$a;b$$, E'quote\';still'; SELECT "quoted;name" FROM t;`
	parts, err := SplitDuckDB(source)
	if err != nil || len(parts) != 3 {
		t.Fatalf("split %+v %v", parts, err)
	}
	if got := DuckDBStatementAt(source, strings.Index(source, "$$")+3); got != parts[1] {
		t.Fatalf("caret %q", got)
	}
	formatted, err := FormatDuckDB(source)
	if err != nil || !strings.Contains(formatted, "$$a;b$$") || !strings.Contains(formatted, `E'quote\';still'`) {
		t.Fatalf("format %q %v", formatted, err)
	}
	if _, err = SplitDuckDB("SELECT $$unterminated"); err == nil {
		t.Fatal("unterminated dollar string accepted")
	}
	if SingleDuckDBStatement("SELECT 1; DELETE FROM records") == nil {
		t.Fatal("multiple EXPLAIN statements accepted")
	}
}

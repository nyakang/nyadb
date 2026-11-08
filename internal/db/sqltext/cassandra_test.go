package sqltext

import (
	"strings"
	"testing"
)

func TestCassandraNativeBatchFunctionsAndComments(t *testing.T) {
	source := `// native comment
BEGIN UNLOGGED BATCH
INSERT INTO "Mixed".rows(id,value) VALUES (1,'a;''b');
UPDATE "Mixed".rows SET value='x' WHERE id=2;
APPLY BATCH;
CREATE FUNCTION f(x int) RETURNS NULL ON NULL INPUT RETURNS int LANGUAGE java AS $$ return x + 1; $$;
SELECT 'back\slash;value',"quoted;name" FROM rows; -- terminal comment`
	parts, err := SplitCassandra(source)
	if err != nil || len(parts) != 3 || !strings.Contains(parts[0], "APPLY BATCH") || !strings.Contains(parts[1], "return x + 1;") {
		t.Fatal("native statements split incorrectly", err)
	}
	if got := CassandraStatementAt(source, strings.Index(source, "UPDATE")); got != parts[0] {
		t.Fatal("caret broke native batch")
	}
	for _, src := range []string{"-- only comment", "BEGIN BATCH INSERT INTO rows(id) VALUES(1);", "SELECT 'unterminated", "CREATE FUNCTION f() AS $$unterminated", "SELECT 1 /* nested /* comment */", "SELECT `name`", "SELECT #comment", "SELECT \x00"} {
		if _, err := SplitCassandra(src); err == nil {
			t.Fatal("unsupported or incomplete CQL accepted")
		}
	}
}

func TestCassandraSourceBoundsRejectWholeScript(t *testing.T) {
	for _, source := range []string{strings.Repeat("SELECT id FROM rows;", 1025), strings.Repeat("x", (8<<20)+1), "SELECT '\xff'", "BEGIN COUNTER BATCH UPDATE counters SET n=n+1 WHERE id=1;"} {
		if parts, err := SplitCassandra(source); err == nil || parts != nil {
			t.Fatal("invalid complete CQL source partially admitted")
		}
	}
	if parts, err := SplitCassandra(strings.Repeat("SELECT id FROM rows;", 1024)); err != nil || len(parts) != 1024 {
		t.Fatal("document statement limit incorrect", err)
	}
	parts, err := SplitCassandra("BEGIN COUNTER BATCH UPDATE counters SET n=n+1 WHERE id=1; APPLY BATCH; SELECT 'APPLY BATCH;' FROM rows")
	if err != nil || len(parts) != 2 {
		t.Fatal("counter batch/source quotes broken", err)
	}
}

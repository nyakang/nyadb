package sqltext

import (
	"strings"
	"testing"
)

func TestPortableSQLDollarEscapesNestedCommentsAndTriggerBodies(t *testing.T) {
	src := `-- leading
CREATE FUNCTION f() RETURNS text AS $body$BEGIN; RETURN 'a;b'; END$body$ LANGUAGE plpgsql;
SELECT E'one\';two'; /* outer /* nested */ retained */ SELECT 2;`
	parts, e := SplitPortable(src)
	if e != nil || len(parts) != 3 {
		t.Fatalf("portable scanner %#v %v", parts, e)
	}
	if got := PortableStatementAt(src, strings.Index(src, "RETURN")); got != parts[0] {
		t.Fatalf("caret split function body %q", got)
	}
	formatted, e := FormatPortable(src)
	if e != nil || !strings.Contains(formatted, `$body$BEGIN; RETURN 'a;b'; END$body$`) || !strings.Contains(formatted, `E'one\';two'`) || !strings.Contains(formatted, "/* outer /* nested */ retained */") {
		t.Fatalf("portable format changed literal/comment %q %v", formatted, e)
	}
	trigger := `CREATE TRIGGER f AFTER INSERT ON t BEGIN INSERT INTO audit VALUES(CASE WHEN NEW.x=1 THEN 'a;b' ELSE 'c' END); UPDATE t SET x=2; END; SELECT 2;`
	parts, e = SplitPortable(trigger)
	if e != nil || len(parts) != 2 {
		t.Fatalf("SQLite trigger body %#v %v", parts, e)
	}
}

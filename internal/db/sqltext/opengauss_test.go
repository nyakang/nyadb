package sqltext

import (
	"strings"
	"testing"
)

func TestOpenGaussNativeBodiesKeepInternalSemicolonsAndCaret(t *testing.T) {
	routine := `-- definition
CREATE OR REPLACE PROCEDURE "odd;name"() AS
DECLARE n INTEGER := 0;
BEGIN
  IF n = 0 THEN
    FOR n IN 1..2 LOOP
      BEGIN
        n := CASE WHEN n = 1 THEN 2 ELSE 3 END;
      END;
    END LOOP;
  END IF;
  /* outer /* END; / */ retained */
  RAISE NOTICE 'long; END; 中文';
END;`
	source := "SELECT 6\n/\n2;\n" + routine + "\n/\n" + `SELECT E'one\';two';`
	parts, err := SplitOpenGauss(source)
	if err != nil || len(parts) != 3 || parts[1] != routine || parts[0] != "SELECT 6\n/\n2" {
		t.Fatalf("native script split: %#v %v", parts, err)
	}
	for _, text := range []string{"DECLARE n", "FOR n", "CASE WHEN", "中文", "END IF"} {
		if got := OpenGaussStatementAt(source, strings.Index(source, text)); got != routine {
			t.Fatalf("caret in %s split definition: %q", text, got)
		}
	}
	formatted, err := FormatOpenGauss(source)
	if err != nil || formatted != source {
		t.Fatal("native formatting rewrote a routine", err)
	}
	parts, err = SplitOpenGauss("CREATE PROCEDURE p() AS BEGIN NULL; END; SELECT 2;")
	if err != nil || len(parts) != 2 || !strings.HasSuffix(parts[0], "END;") {
		t.Fatal("native EOF/caret execution requires slash", parts, err)
	}
}

func TestOpenGaussQuotedBodiesAndCommentsRemainOpaque(t *testing.T) {
	source := `CREATE FUNCTION f() RETURNS text AS $body$ BEGIN; RETURN 'END;'; END $body$ LANGUAGE plpgsql;
/
-- next
CREATE PROCEDURE p() AS BEGIN NULL; END;
  /
SELECT 'it''s;done', "end;name"; -- tail only`
	parts, err := SplitOpenGauss(source)
	if err != nil || len(parts) != 3 || !strings.Contains(parts[0], `$body$ BEGIN; RETURN 'END;'; END $body$`) || !strings.HasSuffix(parts[1], "END;") {
		t.Fatal(parts, err)
	}
	if got := OpenGaussStatementAt(source, strings.Index(source, "RETURN")); got != parts[0] {
		t.Fatal("dollar body caret split", got)
	}
	// Native block recognition must not activate on ordinary SQL names.
	parts, err = SplitOpenGauss(`CREATE TABLE t(procedure text); SELECT 'BEGIN; END;', $tag$a;b$tag$; -- only comment`)
	if err != nil || len(parts) != 2 {
		t.Fatal(parts, err)
	}
}

func TestOpenGaussMalformedScriptsRejectedBeforeExecution(t *testing.T) {
	for _, source := range []string{
		"INSERT INTO t VALUES(1); CREATE PROCEDURE p() AS BEGIN NULL;",
		"CREATE PROCEDURE p() AS BEGIN IF true THEN NULL; END;",
		"CREATE PROCEDURE p() AS BEGIN NULL; END IF;",
		"CREATE PROCEDURE p() AS BEGIN NULL; END",
		"CREATE PROCEDURE p() AS DECLARE n integer;",
		"CREATE PROCEDURE p() AS BEGIN NULL; END; SELECT 'unterminated",
		"CREATE PACKAGE pkg AS PROCEDURE p(); END; /",
		"CREATE PROCEDURE p() AS PROCEDURE child() IS BEGIN NULL; END; BEGIN NULL; END;",
		"SELECT $body$unfinished", "SELECT E'escape\\'", "/* outer /* nested */",
		strings.Repeat("SELECT 1;", 1025), strings.Repeat("x", (8<<20)+1),
	} {
		if parts, err := SplitOpenGauss(source); err == nil || parts != nil {
			t.Fatalf("malformed/unsupported script accepted: %.120q", source)
		}
	}
	// Body semicolons do not count toward the top-level statement budget.
	parts, err := SplitOpenGauss("CREATE PROCEDURE p() AS BEGIN " + strings.Repeat("NULL;", 1100) + " END;")
	if err != nil || len(parts) != 1 {
		t.Fatal("internal semicolon budget", err, len(parts))
	}
}

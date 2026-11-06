package sqltext

import (
	"strings"
	"testing"
)

func TestOracleBlocksAndAlternativeQuotes(t *testing.T) {
	source := `SELECT q'[a;b]' FROM dual;
DECLARE x VARCHAR2(20):=q'{x;y}'; BEGIN IF x IS NOT NULL THEN NULL; END IF; END;
/
CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;
/
SELECT nq'<中文;>' FROM dual;`
	parts, err := SplitOracle(source)
	if err != nil || len(parts) != 4 || !strings.HasSuffix(parts[1], "END;") || strings.Contains(parts[2], "\n/") {
		t.Fatal(parts, err)
	}
	if got := OracleStatementAt(source, strings.Index(source, "IF x")); got != parts[1] {
		t.Fatal(got)
	}
	for _, source := range []string{`SELECT q'[a`, `SELECT 'a`, `/* open`} {
		if _, err := SplitOracle(source); err == nil {
			t.Fatal(source)
		}
	}
	if OracleQuery(`WITH x AS (SELECT 1 FROM dual) DELETE FROM t`) || !OracleQuery(`-- comment
 WITH x AS (SELECT 1 FROM dual) SELECT * FROM x`) {
		t.Fatal("Oracle query classification")
	}
	for _, source := range []string{`DELETE FROM t`, `SELECT 1 FROM dual; SELECT 2 FROM dual`} {
		if err := SingleOracleStatement(source); err == nil {
			t.Fatal(source)
		}
	}
	tokens := completionTokens(`SELECT q'[don't complete WHERE here]' FROM dual`, "oracle")
	protected := false
	for _, tok := range tokens {
		if strings.HasPrefix(tok.text, "q'") {
			protected = tok.protected
		}
	}
	if !protected {
		t.Fatal("q quote completion leak", tokens)
	}
}

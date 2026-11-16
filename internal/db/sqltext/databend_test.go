package sqltext

import (
	"strings"
	"testing"
)

func TestDatabendNativeEscapesBodiesCommentsAndCapturedMutationKinds(t *testing.T) {
	source := `-- original header
SELECT 'a\';still one'; CREATE PROCEDURE original() RETURNS STRING LANGUAGE SQL AS $$ BEGIN RETURN 'body; preserved'; END; $$; /* tail */ INSERT INTO "under_score" VALUES (1); -- unsent comment`
	parts, err := SplitDatabend(source)
	if err != nil || len(parts) != 3 || !strings.Contains(parts[0], "still one") || !strings.Contains(parts[1], "END;") || !strings.HasPrefix(parts[2], "/* tail */ INSERT") {
		t.Fatal("native SQL boundaries changed", err, parts)
	}
	for source, kind := range map[string]string{
		"/*original*/ INSERT INTO t VALUES (1)":                                                  "insert",
		"WITH original(id) AS (SELECT 1), second AS (SELECT id FROM original) UPDATE t SET id=2": "update",
		"WITH original AS (SELECT 1) DELETE FROM t WHERE id=1":                                   "delete",
		"WITH original AS (SELECT 'INSERT; DELETE') SELECT * FROM original":                      "",
		"SELECT 'number of rows inserted'":                                                       "",
	} {
		if got := DatabendMutationKind(source); got != kind {
			t.Fatal("mutation classified from body/display instead of original statement", source, got)
		}
	}
	if parts, err := SplitDatabend("-- only comment\n/* ignored */"); err != nil || len(parts) != 0 {
		t.Fatal("comment-only source dispatched")
	}
	for _, bad := range []string{`SELECT 'unclosed`, `SELECT $$unclosed`, "SELECT 1\x00", string([]byte{0xff})} {
		if _, err := SplitDatabend(bad); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
}

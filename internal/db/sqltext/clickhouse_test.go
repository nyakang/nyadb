package sqltext

import (
	"strings"
	"testing"
)

func TestClickHouseStatementBoundariesDoNotExecuteCommentedSQL(t *testing.T) {
	source := "--heading without a space\nSELECT 'a\\';b', \"odd;name\" --comment; DROP TABLE hidden\n; SELECT $tag$select; FROM text$tag$; #tail; DROP TABLE hidden"
	parts, err := SplitClickHouse(source)
	if err != nil || len(parts) != 2 {
		t.Fatalf("native boundaries %d %v", len(parts), err)
	}
	if !strings.Contains(parts[0], "--comment; DROP TABLE hidden") || !strings.Contains(parts[1], "$tag$select; FROM text$tag$") {
		t.Fatal("protected SQL content changed")
	}
	formatted, err := FormatClickHouse(source)
	if err != nil || !strings.Contains(formatted, "--comment; DROP TABLE hidden") || !strings.Contains(formatted, "$tag$select; FROM text$tag$") {
		t.Fatal("formatting changed a comment or heredoc")
	}
	if got := ClickHouseStatementAt(source, strings.Index(source, "DROP TABLE")); got != parts[0] {
		t.Fatal("caret treated commented SQL as a separate statement")
	}
	if err := SingleClickHouseStatement(source); err == nil {
		t.Fatal("EXPLAIN accepted multiple statements")
	}
	for _, bad := range []string{"SELECT 'unterminated", "SELECT $body$not closed", "SELECT 1 /* outer /* nested */ */"} {
		if _, err = SplitClickHouse(bad); err == nil {
			t.Fatal("unsupported syntax accepted")
		}
	}
	if parts, err = SplitClickHouse("--comment; SELECT 1\n#comment; SELECT 2"); err != nil || len(parts) != 0 {
		t.Fatal("comments-only input became an executable request")
	}
}

func TestClickHouseCompletionProtectsNativeCommentsHeredocsAndQuotedIdentifiers(t *testing.T) {
	for _, source := range []string{"SELECT 1 --comment WHERE users.", "SELECT $body$users."} {
		result := CompleteSQL(source, len(source), completionFixture("clickhouse"))
		if len(result.Items) != 0 {
			t.Fatal("completion escaped protected native text")
		}
	}
	result := completionAt(t, `SELECT u.| FROM "users" u`, "clickhouse")
	if !strings.Contains(strings.Join(completionNames(result), ","), "email") {
		t.Fatal("native double-quoted table did not resolve")
	}
}

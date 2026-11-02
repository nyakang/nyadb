package sqltext

import (
	"slices"
	"strings"
	"testing"
)

func completionFixture(dialect string) CompletionCatalog {
	return CompletionCatalog{Dialect: dialect, Namespace: "app", Namespaces: []string{"app", "other"}, Objects: []CompletionObject{
		{Namespace: "app", Name: "users", Columns: []string{"id", "name", "email", "odd column", "select"}},
		{Namespace: "app", Name: "orders", Columns: []string{"id", "user_id", "amount"}},
		{Namespace: "other", Name: "users", Columns: []string{"other_id", "remote"}},
	}}
}
func completionAt(t *testing.T, marked, dialect string) CompletionResult {
	t.Helper()
	offset := strings.Index(marked, "|")
	if offset < 0 {
		t.Fatal("caret required")
	}
	return CompleteSQL(strings.Replace(marked, "|", "", 1), offset, completionFixture(dialect))
}
func completionNames(result CompletionResult) []string {
	var out []string
	for _, item := range result.Items {
		out = append(out, item.Name)
	}
	return out
}
func TestSQLCompletionAliasNamespaceAndStatementIsolation(t *testing.T) {
	for _, tc := range []struct {
		sql          string
		want, absent []string
	}{
		{"SELECT u.| FROM users AS u JOIN orders o ON u.id=o.user_id", []string{"email", "name"}, []string{"amount", "remote", "SELECT"}},
		{"SELECT o.am| FROM orders o", []string{"amount"}, []string{"email"}},
		{"SELECT app.users.|", []string{"email"}, []string{"remote"}},
		{"SELECT other.users.|", []string{"remote"}, []string{"email"}},
		{"SELECT other.|", []string{"users"}, []string{"orders", "email"}},
		{"SELECT u.| FROM users u; SELECT remote FROM other.users u", []string{"email"}, []string{"remote"}},
		{"SELECT * FROM other.users u; SELECT u.| FROM users u", []string{"email"}, []string{"remote"}},
		{"SELECT missing.| FROM users", nil, []string{"email", "SELECT"}},
		{"SELECT u.| FROM users u JOIN orders u", nil, []string{"email", "amount"}},
		{"SELECT u.| FROM users u UNION SELECT * FROM other.users u", []string{"email"}, []string{"remote"}},
		{"SELECT * FROM users u UNION SELECT u.| FROM other.users u", []string{"remote"}, []string{"email"}},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got := completionNames(completionAt(t, tc.sql, "mysql"))
			for _, name := range tc.want {
				if !slices.Contains(got, name) {
					t.Fatalf("missing %q in %v", name, got)
				}
			}
			for _, name := range tc.absent {
				if slices.Contains(got, name) {
					t.Fatalf("unrelated %q in %v", name, got)
				}
			}
		})
	}
}
func TestSQLCompletionCTEDerivedAndCorrelatedScopes(t *testing.T) {
	for _, tc := range []struct {
		sql          string
		want, absent []string
	}{
		{"WITH c(customer, total) AS (SELECT user_id, amount FROM orders) SELECT c.| FROM c", []string{"customer", "total"}, []string{"email"}},
		{"WITH c AS (SELECT user_id AS customer, amount FROM orders) SELECT c.| FROM c", []string{"customer", "amount"}, []string{"email"}},
		{"SELECT d.| FROM (SELECT id AS customer, name FROM users) d", []string{"customer", "name"}, []string{"email", "id"}},
		{"SELECT d.| FROM (SELECT id + user_id, amount FROM orders) d", []string{"amount"}, []string{"user_id"}},
		{"SELECT * FROM users u WHERE EXISTS (SELECT u.| FROM orders o)", []string{"email"}, []string{"amount"}},
		{"SELECT * FROM users u WHERE EXISTS (SELECT u.| FROM orders u)", []string{"amount"}, []string{"email"}},
		{"SELECT u.| FROM users u WHERE EXISTS (SELECT * FROM orders u)", []string{"email"}, []string{"amount"}},
		{"SELECT (u.|) FROM users u", []string{"email"}, []string{"amount"}},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got := completionNames(completionAt(t, tc.sql, "postgres"))
			for _, name := range tc.want {
				if !slices.Contains(got, name) {
					t.Fatalf("missing %q in %v", name, got)
				}
			}
			for _, name := range tc.absent {
				if slices.Contains(got, name) {
					t.Fatalf("unrelated %q in %v", name, got)
				}
			}
		})
	}
}
func TestSQLCompletionProtectedTextAndDialect(t *testing.T) {
	for _, tc := range []struct{ dialect, sql string }{
		{"mysql", "SELECT 'u.|' FROM users u"},
		{"mysql", "SELECT \"u.|\" FROM users u"},
		{"mysql", "SELECT 1 # u.|"},
		{"postgres", "SELECT 1 --u.|"},
		{"postgres", "SELECT $body$u.|$body$ FROM users u"},
		{"postgres", "SELECT E'escaped\\'u.|' FROM users u"},
		{"postgres", "SELECT /* nested /* u.| */ */ id FROM users u"},
		{"sqlite", "SELECT /* unfinished u.|"},
	} {
		if got := completionAt(t, tc.sql, tc.dialect); len(got.Items) > 0 {
			t.Fatalf("completion in protected %s text %q: %v", tc.dialect, tc.sql, got.Items)
		}
	}
	for _, tc := range []struct{ dialect, want, absent string }{{"mysql", "AUTO_INCREMENT", "ILIKE"}, {"postgres", "ILIKE", "PRAGMA"}, {"sqlite", "PRAGMA", "AUTO_INCREMENT"}} {
		got := completionNames(completionAt(t, "|", tc.dialect))
		if !slices.Contains(got, tc.want) || slices.Contains(got, tc.absent) {
			t.Fatalf("wrong dialect keywords %s: %v", tc.dialect, got)
		}
	}
	got := completionNames(completionAt(t, "SELECT * FROM |", "postgres"))
	if !slices.Contains(got, "users") || slices.Contains(got, "SELECT") || slices.Contains(got, "email") {
		t.Fatalf("FROM did not restrict to relations: %v", got)
	}
}
func TestSQLCompletionQuotesUnicodeAndUnknownMetadata(t *testing.T) {
	for _, tc := range []struct{ dialect, sql, name, insert string }{
		{"mysql", "SELECT u.odd| FROM users u", "odd column", "`odd column`"},
		{"postgres", "SELECT u.odd| FROM users u", "odd column", "\"odd column\""},
		{"mysql", "SELECT * FROM users u WHERE u.`odd|", "odd column", "`odd column`"},
		{"postgres", "SELECT u.\"odd|\" FROM users u", "odd column", "\"odd column"},
		{"mysql", "SELECT u.se| FROM users u", "select", "`select`"},
	} {
		got := completionAt(t, tc.sql, tc.dialect)
		found := false
		for _, item := range got.Items {
			if item.Name == tc.name {
				found = item.Insert == tc.insert
			}
		}
		if !found {
			t.Fatalf("missing quoted completion for %q: %+v", tc.sql, got)
		}
	}
	catalog := completionFixture("postgres")
	catalog.Objects = append(catalog.Objects, CompletionObject{Namespace: "app", Name: "客户", Columns: []string{"姓名"}})
	sql := "SELECT 客.姓 FROM 客户 客"
	got := CompleteSQL(sql, len("SELECT 客.姓"), catalog)
	if names := completionNames(got); !slices.Contains(names, "姓名") || got.Start != len("SELECT 客.") {
		t.Fatalf("UTF-8 offsets/identifiers: %+v", got)
	}
	catalog.Objects = append(catalog.Objects, CompletionObject{Namespace: "app", Name: "unknown"})
	if got := CompleteSQL("SELECT u. FROM unknown u", len("SELECT u."), catalog); len(got.Items) != 0 {
		t.Fatalf("unknown metadata acquired columns: %v", got.Items)
	}
}

func TestSQLCompletionBoundsParsingAndPreservesPostgresCase(t *testing.T) {
	catalog := completionFixture("postgres")
	catalog.Objects[0].Columns = append(catalog.Objects[0].Columns, "CaseSensitive")
	result := CompleteSQL("SELECT u.Cas FROM users u", len("SELECT u.Cas"), catalog)
	if len(result.Items) != 1 || result.Items[0].Insert != "\"CaseSensitive\"" {
		t.Fatalf("mixed-case PostgreSQL identifier folded: %+v", result)
	}
	for _, source := range []string{strings.Repeat(" ", 257<<10), strings.Repeat("(", 129) + "SELECT u. FROM users u"} {
		if result := CompleteSQL(source, len(source), catalog); len(result.Items) != 0 {
			t.Fatal("completion exceeded its UI-thread parsing bound")
		}
	}
}

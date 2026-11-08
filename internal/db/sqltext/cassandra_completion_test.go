package sqltext

import (
	"strings"
	"testing"
)

func TestCassandraCompletionNativeSyntaxAndExactIdentity(t *testing.T) {
	catalog := CompletionCatalog{Dialect: "cassandra", Namespace: "app", Namespaces: []string{"app", "MixedCase"}, Objects: []CompletionObject{{Namespace: "app", Name: "items", Columns: []string{"id", "value"}}, {Namespace: "MixedCase", Name: "Items", Columns: []string{"ExactColumn"}}}}
	for _, tc := range []struct{ source, want, absent string }{
		{"SELECT val| FROM app.items", "value", "JOIN"},
		{"SELECT Ex| FROM \"MixedCase\".\"Items\"", "ExactColumn", "EXPLAIN"},
		{"SELECT Ex| FROM MixedCase.Items", "", "ExactColumn"},
		{"SELECT * FROM \"MixedCase\".I|", "Items", "items"},
		{"SELECT * FROM app.items WHERE 'val|';", "", "value"},
		{"CREATE FUNCTION f() RETURNS NULL ON NULL INPUT RETURNS text LANGUAGE java AS $$ val| $$;", "", "value"},
		{"BE|", "BEGIN", "BETWEEN"},
		{"JO|", "", "JOIN"},
	} {
		offset := strings.IndexByte(tc.source, '|')
		source := strings.Replace(tc.source, "|", "", 1)
		r := CompleteSQL(source, offset, catalog)
		names := map[string]string{}
		for _, item := range r.Items {
			names[item.Name] = item.Insert
		}
		if tc.want != "" && names[tc.want] == "" {
			t.Fatal("missing CQL suggestion", tc.source, names)
		}
		if tc.absent != "" && names[tc.absent] != "" {
			t.Fatal("unsupported or foreign CQL suggestion", tc.source, names)
		}
		if tc.want == "ExactColumn" && names[tc.want] != "\"ExactColumn\"" {
			t.Fatal("quoted CQL identifier lost")
		}
	}
}

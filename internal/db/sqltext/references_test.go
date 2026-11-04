package sqltext

import (
	"fmt"
	"testing"
)

func TestReferencesIncludeCommaRelationsAndSkipLocalExpressions(t *testing.T) {
	refs := ReferencedObjects("WITH local AS (SELECT * FROM seed) SELECT f(x,y), a.id FROM accounts a, tenant.orders b JOIN local c ON a.id=c.id WHERE a.id IN (1,2)", "postgresql", "public")
	names := []string{}
	for _, ref := range refs {
		names = append(names, ref.Namespace+"."+ref.Name)
	}
	if fmt.Sprint(names) != "[public.seed public.accounts tenant.orders]" {
		t.Fatal(names)
	}
}

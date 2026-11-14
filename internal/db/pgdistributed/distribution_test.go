package pgdistributed_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/pgdistributed"
)

func TestNativeDistributionCatalogCodesAndIdentifierOrder(t *testing.T) {
	for _, test := range []struct {
		product pgdistributed.Product
		code    string
		columns []string
		want    string
	}{
		{pgdistributed.Cloudberry, "p", nil, "DISTRIBUTED RANDOMLY"},
		{pgdistributed.Cloudberry, "r", nil, "DISTRIBUTED REPLICATED"},
		{pgdistributed.Cloudberry, "p", []string{"tenant_id", "order id"}, `DISTRIBUTED BY ("tenant_id", "order id")`},
		{pgdistributed.OpenTenBase, "H", []string{"tenant"}, `DISTRIBUTE BY HASH ("tenant")`},
		{pgdistributed.OpenTenBase, "M", []string{"tenant"}, `DISTRIBUTE BY MODULO ("tenant")`},
		{pgdistributed.OpenTenBase, "S", []string{"tenant"}, `DISTRIBUTE BY SHARD ("tenant")`},
		{pgdistributed.OpenTenBase, "R", nil, "DISTRIBUTE BY REPLICATION"},
		{pgdistributed.OpenTenBase, "N", nil, "DISTRIBUTE BY ROUNDROBIN"},
		{pgdistributed.Cloudberry, "p", []string{"a\nb", "a,b", "\"; DROP TABLE t; --"}, "DISTRIBUTED BY (\"a\nb\", \"a,b\", \"\"\"; DROP TABLE t; --\")"},
	} {
		got, err := (pgdistributed.Policy{Product: test.product, Code: test.code, Columns: test.columns}).Clause()
		if err != nil || got != test.want {
			t.Fatal(got, err, test.want)
		}
	}
}

func TestDistributionRejectsUnknownIncompleteOrAmbiguousMetadata(t *testing.T) {
	for _, p := range []pgdistributed.Policy{
		{Product: pgdistributed.Cloudberry, Code: "unexpected"},
		{Product: pgdistributed.OpenTenBase, Code: "D"},
		{Product: "postgres", Code: "H", Columns: []string{"id"}},
	} {
		if _, err := p.Clause(); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("unknown native semantics accepted", err)
		}
	}
	for _, p := range []pgdistributed.Policy{
		{Product: pgdistributed.Cloudberry, Code: "r", Columns: []string{"id"}},
		{Product: pgdistributed.OpenTenBase, Code: "H"},
		{Product: pgdistributed.OpenTenBase, Code: "S", Columns: []string{"id", "id"}},
		{Product: pgdistributed.Cloudberry, Code: "p", Columns: []string{""}},
		{Product: pgdistributed.Cloudberry, Code: "p", Columns: []string{"a\x00b"}},
		{Product: pgdistributed.Cloudberry, Code: "p", Columns: []string{"\xff"}},
		{Product: pgdistributed.Cloudberry, Code: "p", Columns: []string{strings.Repeat("x", 1025)}},
		{Product: pgdistributed.Cloudberry, Code: "p", Columns: make([]string, 129)},
	} {
		if _, err := p.Clause(); !errors.Is(err, pgdistributed.ErrPolicy) {
			t.Fatal("incomplete metadata accepted", err)
		}
	}
}

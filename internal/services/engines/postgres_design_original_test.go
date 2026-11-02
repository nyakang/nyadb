package engines

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func pgOriginalSeedFixture() *schemaddl.PostgreSQLCatalog {
	return &schemaddl.PostgreSQLCatalog{ServerVersion: 170000, Database: "db", Namespace: "schema", DatabaseOID: 10, NamespaceOID: 11, RelationOID: 700, Entries: []schemaddl.PostgreSQLCatalogEntry{
		{Kind: "database", OID: 10, Data: json.RawMessage(`{"comment":"SQL and secrets stay out of the seed parameters"}`)},
		{Kind: "namespace", OID: 11, Data: json.RawMessage(`{"name":"schema"}`)},
		{Kind: "relation", OID: 700, Data: json.RawMessage(`{"oid":700,"relname":"not_a_seed_label"}`)},
		{Kind: "column", OID: 700, SubID: 4, Data: json.RawMessage(`{"attname":"not_a_seed_label"}`)},
		{Kind: "type", OID: 700, Data: json.RawMessage(`{"typname":"different_catalog_same_number"}`)},
		{Kind: "enum", OID: 999, Data: json.RawMessage(`{"enumtypid":"800","enumlabel":"not_a_seed_label"}`)},
		{Kind: "range", OID: 800, Data: json.RawMessage(`{"rngtypid":800}`)},
		{Kind: "aggregate", OID: 900, Data: json.RawMessage(`{"aggfnoid":900}`)},
		{Kind: "routine", OID: 900, Data: json.RawMessage(`{"prosrc":"SQL and secrets stay out of the seed parameters"}`)},
		{Kind: "dependency", OID: 700, Data: json.RawMessage(`{"edges":[{"classid":1259,"objid":700},{"classid":1247,"objid":"700"},{"classid":1247,"objid":"700"}]}`)},
		{Kind: "security_label:1259", OID: 700, SubID: 4, Data: json.RawMessage(`{"labels":[{"provider":"fixture","label":"not_a_seed_label"}]}`)},
	}}
}

func TestPostgreSQLOriginalSeedsKeepCatalogClassesAndOwnerIdentities(t *testing.T) {
	original := pgOriginalSeedFixture()
	before := schemaddl.Signature(original)
	raw, err := postgresOriginalCatalogSeeds(original)
	if err != nil {
		t.Fatal(err)
	}
	var seeds []postgresOriginalSeed
	if json.Unmarshal([]byte(raw), &seeds) != nil || len(seeds) != 6 {
		t.Fatal("original seed identities were missing or duplicated", raw)
	}
	for _, wanted := range []postgresOriginalSeed{
		{Catalog: "pg_class", OID: 700}, {Catalog: "pg_type", OID: 700}, {Catalog: "pg_type", OID: 800}, {Catalog: "pg_proc", OID: 900}, {ClassOID: 1259, OID: 700}, {ClassOID: 1247, OID: 700},
	} {
		if !slices.Contains(seeds, wanted) {
			t.Fatal("same-number catalog objects or subordinate owners were confused", wanted)
		}
	}
	if strings.Contains(raw, "not_a_seed_label") || strings.Contains(raw, "SQL and secrets") || strings.Contains(raw, "999") || schemaddl.Signature(original) != before {
		t.Fatal("seeds contained raw values, fictitious owner OIDs or mutated evidence")
	}
	slices.Reverse(original.Entries)
	reordered, err := postgresOriginalCatalogSeeds(original)
	if err != nil || raw != reordered {
		t.Fatal("seed parameters changed with record order", err)
	}
	if raw, err := postgresOriginalCatalogSeeds(nil); err != nil || raw != "[]" {
		t.Fatal("ordinary snapshot acquired original-context seeds", err)
	}
}

func TestPostgreSQLOriginalSeedsRejectInvalidOrUnsupportedEvidence(t *testing.T) {
	for _, change := range []func(*schemaddl.PostgreSQLCatalog){
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[0].OID++ },
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[1].SubID++ },
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[2].OID = 1 << 32 },
		func(p *schemaddl.PostgreSQLCatalog) {
			p.Entries[5].Data = json.RawMessage(`{"enumtypid":99999999999999999999}`)
		},
		func(p *schemaddl.PostgreSQLCatalog) {
			p.Entries[9].Data = json.RawMessage(`{"edges":[{"classid":1259,"objid":701}]}`)
		},
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[9].Data = json.RawMessage(`{"edges":null}`) },
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[10].Kind = "security_label:0" },
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[10].Kind = "security_label:4294967296" },
		func(p *schemaddl.PostgreSQLCatalog) { p.Entries[10].Kind = "unknown_private_text" },
	} {
		p := pgOriginalSeedFixture()
		change(p)
		if raw, err := postgresOriginalCatalogSeeds(p); err == nil || raw != "" || strings.Contains(err.Error(), "99999999999999999999") || strings.Contains(err.Error(), "unknown_private_text") {
			t.Fatal("invalid original scope produced parameters or leaked values", err)
		}
	}
	for _, raw := range []string{`0`, `-1`, `null`, `1.5`, `7e2`, `" 700"`, `"4294967296"`, `true`} {
		if _, err := postgresOriginalOID(json.RawMessage(raw)); err == nil {
			t.Fatal("unbound/invalid original OID accepted", raw)
		}
	}
	unsupportedVersion := pgOriginalSeedFixture()
	unsupportedVersion.ServerVersion = 140000
	unsupportedVersion.Entries = append(unsupportedVersion.Entries, schemaddl.PostgreSQLCatalogEntry{Kind: "publication_namespace", OID: 8000, Data: json.RawMessage(`{}`)})
	if raw, err := postgresOriginalCatalogSeeds(unsupportedVersion); err == nil || raw != "" {
		t.Fatal("seed resolved an unavailable PostgreSQL catalog class", err)
	}
}

func TestPostgreSQLOriginalScopeAndCancellationFailBeforeDatabaseReads(t *testing.T) {
	p := pgOriginalSeedFixture()
	target := contract.SchemaDesignTarget{Object: contract.ObjectRef{Catalog: "other", Schema: "schema", Kind: "table", Name: "t"}}
	if partial, err := postgresCatalogSnapshotPreserving(context.Background(), nil, target, p); !errors.Is(err, schemaddl.ErrConflict) || partial.Schema.Postgres != nil {
		t.Fatal("wrong original scope reached the database or published partial evidence", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if partial, err := postgresCatalogSnapshotPreserving(ctx, nil, target, p); !errors.Is(err, context.Canceled) || partial.Schema.Postgres != nil {
		t.Fatal("cancelled original scope reached the database", err)
	}
}

func TestPostgreSQLOriginalSeedQueryRemainsVersionAware(t *testing.T) {
	if strings.Contains(postgresDesignCatalogFor(140000), "pg_publication_namespace") || !strings.Contains(postgresDesignCatalogFor(170000), "pg_publication_namespace") {
		t.Fatal("original-context query required an unavailable catalog")
	}
}

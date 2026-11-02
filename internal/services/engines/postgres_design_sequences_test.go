package engines

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func pgSequenceEvidence() *schemaddl.PostgreSQLCatalog {
	return &schemaddl.PostgreSQLCatalog{ServerVersion: 170000, Database: "database", Namespace: "schema", DatabaseOID: 1, NamespaceOID: 2, RelationOID: 3, Entries: []schemaddl.PostgreSQLCatalogEntry{
		{Kind: "sequence", OID: 5, Data: json.RawMessage(`{"seqrelid":"5","seqstart":9007199254740993}`)},
		{Kind: "relation", OID: 5, Data: json.RawMessage(`{"oid":5,"namespace":"outside","relname":"seq\"quoted;","relkind":"S"}`)},
		{Kind: "sequence", OID: 4, Data: json.RawMessage(`{"seqrelid":4,"seqstart":-9007199254740993}`)},
		{Kind: "relation", OID: 4, Data: json.RawMessage(`{"oid":"4","namespace":"schema","relname":"original","relkind":"S"}`)},
	}}
}

func TestPostgreSQLSequenceSourcesRequireCompleteStableIdentity(t *testing.T) {
	catalog := pgSequenceEvidence()
	before, _ := catalog.Fingerprint()
	sources, err := postgresSequenceSources(catalog)
	if err != nil || len(sources) != 2 || sources[0].OID != 4 || sources[1].OID != 5 || sources[1].Name != `seq"quoted;` || sources[1].Namespace != "outside" {
		t.Fatal("sequence identity was inferred from order or target namespace", err)
	}
	after, _ := catalog.Fingerprint()
	if before != after {
		t.Fatal("sequence extraction mutated catalog evidence")
	}
	for name, edit := range map[string]func(*schemaddl.PostgreSQLCatalog){
		"missing sequence":   func(c *schemaddl.PostgreSQLCatalog) { c.Entries = c.Entries[1:] },
		"missing relation":   func(c *schemaddl.PostgreSQLCatalog) { c.Entries = append(c.Entries[:1], c.Entries[2:]...) },
		"wrong sequence OID": func(c *schemaddl.PostgreSQLCatalog) { c.Entries[0].Data = json.RawMessage(`{"seqrelid":4}`) },
		"wrong relation OID": func(c *schemaddl.PostgreSQLCatalog) {
			c.Entries[1].Data = json.RawMessage(`{"oid":4,"namespace":"outside","relname":"original","relkind":"S"}`)
		},
		"wrong kind": func(c *schemaddl.PostgreSQLCatalog) {
			c.Entries[1].Data = json.RawMessage(`{"oid":5,"namespace":"outside","relname":"original","relkind":"r"}`)
		},
		"missing namespace": func(c *schemaddl.PostgreSQLCatalog) {
			c.Entries[1].Data = json.RawMessage(`{"oid":5,"relname":"original","relkind":"S"}`)
		},
		"subobject": func(c *schemaddl.PostgreSQLCatalog) { c.Entries[0].SubID = 1 },
		"duplicate": func(c *schemaddl.PostgreSQLCatalog) { c.Entries = append(c.Entries, c.Entries[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			c := catalog.Clone()
			edit(c)
			if sources, err := postgresSequenceSources(c); err == nil || sources != nil {
				t.Fatal("invalid sequence evidence returned a partial baseline")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	empty := catalog.Clone()
	empty.Entries = nil
	if states, err := postgresSequenceStates(ctx, nil, empty); !errors.Is(err, context.Canceled) || states != nil {
		t.Fatal("cancelled empty read produced completed evidence")
	}
}

func TestPostgreSQLSequenceComparisonKeepsExactValuesCalledStateAndIdentity(t *testing.T) {
	before := map[int64]postgresDesignSequence{
		4: {OID: 4, Namespace: "schema", Name: "original", LastValue: -9007199254740993, Called: false},
		5: {OID: 5, Namespace: "outside", Name: `seq"quoted;`, LastValue: 9007199254740993, Called: true},
	}
	if err := postgresCompareSequenceStates(before, maps.Clone(before), nil); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(map[int64]postgresDesignSequence){
		"value":          func(c map[int64]postgresDesignSequence) { x := c[5]; x.LastValue--; c[5] = x },
		"negative value": func(c map[int64]postgresDesignSequence) { x := c[4]; x.LastValue++; c[4] = x },
		"called":         func(c map[int64]postgresDesignSequence) { x := c[4]; x.Called = true; c[4] = x },
	} {
		t.Run(name, func(t *testing.T) {
			after := maps.Clone(before)
			edit(after)
			err := postgresCompareSequenceStates(before, after, nil)
			if !errors.Is(err, schemaddl.ErrDataChanged) || strings.Contains(err.Error(), "900719925474099") {
				t.Fatal("sequence change was ignored or private values were logged", err)
			}
		})
	}
	for name, edit := range map[string]func(map[int64]postgresDesignSequence){
		"missing":         func(c map[int64]postgresDesignSequence) { delete(c, 5) },
		"name":            func(c map[int64]postgresDesignSequence) { x := c[5]; x.Name = "replacement"; c[5] = x },
		"namespace":       func(c map[int64]postgresDesignSequence) { x := c[5]; x.Namespace = "other"; c[5] = x },
		"replacement OID": func(c map[int64]postgresDesignSequence) { x := c[5]; delete(c, 5); x.OID = 6; c[6] = x },
		"wrong key":       func(c map[int64]postgresDesignSequence) { x := c[5]; x.OID = 6; c[5] = x },
	} {
		t.Run(name, func(t *testing.T) {
			after := maps.Clone(before)
			edit(after)
			if err := postgresCompareSequenceStates(before, after, nil); err == nil {
				t.Fatal("replacement sequence acquired the original state identity")
			}
		})
	}
	after := maps.Clone(before)
	delete(after, 5)
	if err := postgresCompareSequenceStates(before, after, map[int64]bool{5: true}); err != nil {
		t.Fatal("authorized original sequence removal failed", err)
	}
	for _, permissions := range []map[int64]bool{{6: true}, {5: false}} {
		if err := postgresCompareSequenceStates(before, after, permissions); err == nil {
			t.Fatal("unknown or false removal permission bypassed verification")
		}
	}
	if err := postgresCompareSequenceStates(before, before, map[int64]bool{5: true}); err == nil {
		t.Fatal("requested removal retained a sequence")
	}
	if err := postgresCompareSequenceStates(nil, before, nil); err == nil {
		t.Fatal("missing evidence treated as an empty original")
	}
}

package schemaddl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func nativeCopyTestSnapshot(t *testing.T) Snapshot {
	t.Helper()
	p := nativeCatalogTest()
	p.Namespace, p.Name = "public", "original"
	p.Entries[0].Data = []byte(`{"nspname":"public","nspacl":null}`)
	p.Entries[1].Data = []byte(`{"relname":"original","relkind":"r","relpersistence":"p","relowner":4,"relacl":null}`)
	p.Entries = append(p.Entries, OpenGaussCatalogEntry{Kind: "column", OID: 3, SubID: 1, Data: []byte(`{"attname":"id","attnum":1,"attacl":null,"attstattarget":-1,"attstorage":"p","attnotnull":true}`)})
	d := OpenGaussCopyDefinition{Owner: "owner", Creator: "owner", CanCreate: true, Columns: []OpenGaussCopyColumn{{Number: 1, Name: "id", Type: "bigint", Default: "nextval('public.original_seq'::regclass)"}}, Sequences: []OpenGaussCopySequence{{OID: 5, Column: 1, Schema: "public", Name: "original_seq", Owner: "owner", Start: "1", Increment: "1", Min: "1", Max: "9223372036854775807", Cache: "1"}}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	p.Entries = append(p.Entries, OpenGaussCatalogEntry{Kind: "copy_definition", OID: 3, Data: raw})
	revision, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Engine: "opengauss", Exists: true, Revision: revision, Table: Table{Name: "original"}, OpenGauss: &p}
}

func TestOpenGaussCopyPreviewKeepsQualifiedNamesAndRejectsChangedOriginal(t *testing.T) {
	s := nativeCopyTestSnapshot(t)
	p, err := PlanOpenGaussCopy(s, `q"; DROP TABLE other; --`, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.DataSQL, Quote(`q"; DROP TABLE other; --`)) || !strings.Contains(strings.Join(p.SQL, "\n"), "EXCLUDING DEFAULTS") || strings.Contains(strings.Join(p.SQL, "\n"), "IF NOT EXISTS") {
		t.Fatal("copy preview lost native identity/no overwrite semantics")
	}
	changed := s.Clone()
	changed.OpenGauss.Entries[1].Data = []byte(`{"relname":"replacement"}`)
	if _, err := PlanOpenGaussCopy(changed, "copy", true, true); !errors.Is(err, ErrConflict) {
		t.Fatal("changed native record accepted", err)
	}
	if _, err := PlanOpenGaussCopy(s, "original", true, true); err == nil {
		t.Fatal("self overwrite planned")
	}
}

func TestOpenGaussCopyOwnedDefaultAcceptsMixedIdentifierQuotingAndRejectsExpressions(t *testing.T) {
	s := OpenGaussCopySequence{Schema: "public", Name: `q"; DROP TABLE other; --`}
	for _, ref := range []string{Quote(s.Schema) + "." + Quote(s.Name), s.Schema + "." + Quote(s.Name)} {
		c := OpenGaussCopyColumn{Default: "nextval('" + strings.ReplaceAll(ref, "'", "''") + "'::regclass)"}
		if _, err := gaussCopyDefault(c, s, "public", "new_sequence"); err != nil {
			t.Fatal(err)
		}
		c.Default += "+1"
		if _, err := gaussCopyDefault(c, s, "public", "new_sequence"); err == nil {
			t.Fatal("arbitrary owned default silently rewritten")
		}
	}
}

func TestOpenGaussCopyShapePreservesUnknownFieldsAndDoesNotAliasInput(t *testing.T) {
	s := nativeCopyTestSnapshot(t)
	original := Signature(s)
	a, err := OpenGaussCopyShape(s, "copied", true)
	if err != nil {
		t.Fatal(err)
	}
	if Signature(s) != original {
		t.Fatal("shape normalization modified original")
	}
	x := s.Clone()
	x.OpenGauss.Entries[1].Data = []byte(`{"relname":"original","relkind":"r","relpersistence":"p","relowner":4,"relacl":null,"native_unknown":9007199254740993}`)
	b, err := OpenGaussCopyShape(x, "copied", true)
	if err != nil || a == b {
		t.Fatal("unknown native property ignored", err)
	}
}

func TestOpenGaussCopyExpressionDispatchRetainsNativeEmptyCursorAndNestedCalls(t *testing.T) {
	raw := `{FUNCEXPR :funcid 1703 :args ({OPEXPR :opfuncid 1726 :args ({VAR :varattno 1} {CONST :consttype 23 :constvalue 4 [ 2 0 0 0 ] :cursor_data  :row_count 0})})}`
	ids, err := OpenGaussCopyExpressionRoutines(raw)
	if err != nil || len(ids) != 2 || ids[0] != 1703 || ids[1] != 1726 {
		t.Fatal("native resolved calls missing", ids, err)
	}
	if _, err := PostgreSQLValueExpressionRoutines(raw); err == nil {
		t.Fatal("native empty field weakened PostgreSQL validation")
	}
	for _, node := range []string{`{FUNCEXPR :funcid :args <>}`, `{CONST :unknown :row_count 0}`, `{UNKNOWN :args ({FUNCEXPR :funcid 1703})}`, `{OPEXPR :opfuncid 0}`, `{NEXTVALUEEXPR :seqid 5}`} {
		if _, err := OpenGaussCopyExpressionRoutines(node); err == nil {
			t.Fatal("unproven native expression accepted", node)
		}
	}
}

func TestOpenGaussCopySequenceBoundsRemainExact(t *testing.T) {
	s := OpenGaussCopySequence{Start: "9007199254740993", Increment: "-1", Min: "-170141183460469231731687303715884105727", Max: "170141183460469231731687303715884105727", Cache: "7", Large: true}
	if !s.valid() {
		t.Fatal("exact native large sequence bounds rejected")
	}
	s.Cache = "0"
	if s.valid() {
		t.Fatal("zero cache accepted")
	}
	s.Cache = "1"
	s.Max = "170141183460469231731687303715884105728"
	if s.valid() {
		t.Fatal("overflowing native bounds accepted")
	}
}

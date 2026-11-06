package schemaddl

import (
	"encoding/json"
	"slices"
	"testing"
)

func nativeCatalogTest() OpenGaussCatalog {
	return OpenGaussCatalog{Database: "db", Namespace: "a\"b", Name: "table", DatabaseOID: 1, NamespaceOID: 2, RelationOID: 3, RoleOID: 4, CanManage: true,
		Entries: []OpenGaussCatalogEntry{
			{Kind: "namespace", OID: 2, Data: json.RawMessage(`{"nspname":"a\"b","nspacl":null}`)},
			{Kind: "relation", OID: 3, Data: json.RawMessage(`{"relname":"table","relkind":"r","relpages":5,"relacl":["owner=arwdDxt/owner"],"native_unknown":9007199254740993}`)},
			{Kind: "object", OID: 3, Data: json.RawMessage(`{"creator":4,"ctime":"original","mtime":"now","changecsn":5,"createcsn":1}`)},
		}}
}

func TestOpenGaussCatalogCanonicalNumbersAndPhysicalStatistics(t *testing.T) {
	p := nativeCatalogTest()
	a, err := p.Fingerprint()
	if err != nil || a == "" {
		t.Fatal(err)
	}
	x := p.Clone()
	x.Entries[1].Data = json.RawMessage(`{ "native_unknown":9007199254740993,"relacl":["owner=arwdDxt/owner"],"relpages":500,"relkind":"r","relname":"table" }`)
	x.Entries[2].Data = json.RawMessage(`{"creator":4,"ctime":"original","mtime":"later","changecsn":500,"createcsn":1}`)
	slices.Reverse(x.Entries)
	if b, err := x.Fingerprint(); err != nil || a != b {
		t.Fatal("format/order/statistics changed schema revision", err)
	}
	for _, replacement := range []string{
		`{"relname":"table","relkind":"r","relacl":["owner=r/owner"],"native_unknown":9007199254740993}`,
		`{"relname":"table","relkind":"r","relacl":["owner=arwdDxt/owner"],"native_unknown":9007199254740992}`,
	} {
		x := p.Clone()
		x.Entries[1].Data = json.RawMessage(replacement)
		if b, err := x.Fingerprint(); err != nil || a == b {
			t.Fatal("permissions or native integer change ignored", err)
		}
	}
	// Asynchronous request clones retain independent native byte slices.
	snap := Snapshot{Engine: "opengauss", OpenGauss: &p}
	cloned := snap.Clone()
	cloned.OpenGauss.Entries[0].Data[0] = '['
	if p.Entries[0].Data[0] != '{' {
		t.Fatal("native catalog clone aliases worker input")
	}
}

func TestOpenGaussCatalogRejectsIncompleteDuplicateAndMalformedRecords(t *testing.T) {
	for _, mutate := range []func(*OpenGaussCatalog){
		func(p *OpenGaussCatalog) { p.Entries = p.Entries[1:] },
		func(p *OpenGaussCatalog) { p.Entries = p.Entries[:1] },
		func(p *OpenGaussCatalog) { p.Entries = append(p.Entries, p.Entries[0]) },
		func(p *OpenGaussCatalog) { p.Entries[0].Data = json.RawMessage(`null`) },
		func(p *OpenGaussCatalog) { p.Entries[0].Data = json.RawMessage(`{} {}`) },
		func(p *OpenGaussCatalog) { p.Entries[0].Data = json.RawMessage(`{} garbage`) },
		func(p *OpenGaussCatalog) { p.Entries[0].SubID = -1 },
		func(p *OpenGaussCatalog) { p.RoleOID = 0 },
	} {
		p := nativeCatalogTest()
		mutate(&p)
		if _, err := p.Fingerprint(); err == nil {
			t.Fatal("incomplete native catalog became confirmation baseline")
		}
	}
	p := nativeCatalogTest()
	p.RelationOID, p.CanManage, p.Entries = 0, false, p.Entries[:1]
	if _, err := p.Fingerprint(); err != nil {
		t.Fatal("absent table receipt rejected", err)
	}
}

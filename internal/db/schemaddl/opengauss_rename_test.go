package schemaddl

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func nativeRenameTestSnapshot(t *testing.T, name string) Snapshot {
	t.Helper()
	p := nativeCatalogTest()
	p.Namespace, p.Name = "public", name
	p.Entries[0].Data = json.RawMessage(`{"nspname":"public","nspacl":null}`)
	p.Entries[1].Data, _ = json.Marshal(map[string]any{"relname": name, "relkind": "r", "relpersistence": "p", "reloftype": 0, "reltype": 5, "relfilenode": 9, "relowner": 4, "native_unknown": json.Number("9007199254740993")})
	p.Entries = append(p.Entries,
		OpenGaussCatalogEntry{Kind: "type", OID: 5, Data: json.RawMessage(`{"typname":"` + name + `","typarray":6,"typrelid":3}`)},
		OpenGaussCatalogEntry{Kind: "type", OID: 6, Data: json.RawMessage(`{"typname":"_` + name + `","typelem":5}`)},
		OpenGaussCatalogEntry{Kind: "partition", OID: 7, Data: json.RawMessage(`{"relname":"` + name + `","parentid":3,"parttype":"r","relfilenode":11}`)},
		OpenGaussCatalogEntry{Kind: "partition", OID: 8, Data: json.RawMessage(`{"relname":"p1","parentid":3,"parttype":"p","relfilenode":12}`)},
	)
	revision, err := p.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Engine: "opengauss", Exists: true, Revision: revision, Table: Table{Name: name}, OpenGauss: &p}
}

func TestOpenGaussRenameQualifiedPreviewAndCompleteIdentity(t *testing.T) {
	original, actual := nativeRenameTestSnapshot(t, "original"), nativeRenameTestSnapshot(t, "renamed")
	signature := Signature(original)
	sql, err := PlanOpenGaussRename(original, `q"; DROP TABLE other; --`)
	if err != nil || sql != `ALTER TABLE "public"."original" RENAME TO "q""; DROP TABLE other; --"` {
		t.Fatal("qualified single-statement rename", sql, err)
	}
	if err := VerifyOpenGaussRename(original, "renamed", "_renamed", actual); err != nil {
		t.Fatal("native identity retained", err)
	}
	if Signature(original) != signature {
		t.Fatal("preview or proof mutated original")
	}
	for _, field := range []string{"relfilenode", "relowner", "native_unknown"} {
		t.Run(field, func(t *testing.T) {
			x := actual.Clone()
			var record map[string]json.RawMessage
			_ = json.Unmarshal(x.OpenGauss.Entries[1].Data, &record)
			record[field] = json.RawMessage(`9007199254740992`)
			x.OpenGauss.Entries[1].Data, _ = json.Marshal(record)
			x.Revision, _ = x.OpenGauss.Fingerprint()
			if err := VerifyOpenGaussRename(original, "renamed", "_renamed", x); err == nil {
				t.Fatal("changed native identity accepted")
			}
		})
	}
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.OpenGauss.Entries = s.OpenGauss.Entries[:len(s.OpenGauss.Entries)-1] },
		func(s *Snapshot) { s.OpenGauss.Entries[4].Data = json.RawMessage(`{"typname":"_wrong","typelem":5}`) },
		func(s *Snapshot) {
			s.OpenGauss.Entries[6].Data = json.RawMessage(`{"relname":"changed_partition","parentid":3,"parttype":"p","relfilenode":12}`)
		},
		func(s *Snapshot) {
			s.OpenGauss.Entries[5].Data = json.RawMessage(`{"relname":"renamed","parentid":3,"parttype":"r","relfilenode":13}`)
		},
	} {
		x := actual.Clone()
		mutate(&x)
		x.Revision, _ = x.OpenGauss.Fingerprint()
		if err := VerifyOpenGaussRename(original, "renamed", "_renamed", x); err == nil {
			t.Fatal("changed native record accepted")
		}
	}
}

func TestOpenGaussRenameRejectsStaleUnsupportedOrUnmanagedOriginal(t *testing.T) {
	for _, mutate := range []func(*Snapshot){
		func(s *Snapshot) { s.Engine = "postgresql" },
		func(s *Snapshot) { s.Exists = false },
		func(s *Snapshot) { s.OpenGauss.CanManage = false },
		func(s *Snapshot) { s.OpenGauss.Hooks = true },
		func(s *Snapshot) {
			s.OpenGauss.Entries[1].Data = json.RawMessage(`{"relname":"original","relkind":"f","relpersistence":"p"}`)
		},
		func(s *Snapshot) {
			s.OpenGauss.Entries[1].Data = json.RawMessage(`{"relname":"original","relkind":"r","relpersistence":"p","reloftype":5}`)
		},
		func(s *Snapshot) {
			s.OpenGauss.Entries[1].Data = json.RawMessage(`{"relname":"original","relkind":"r","relpersistence":"p","reloptions":["blockchain=true"]}`)
		},
	} {
		s := nativeRenameTestSnapshot(t, "original")
		mutate(&s)
		s.Revision, _ = s.OpenGauss.Fingerprint()
		if _, err := PlanOpenGaussRename(s, "renamed"); err == nil {
			t.Fatal("unsupported native rename accepted")
		}
	}
	s := nativeRenameTestSnapshot(t, "original")
	s.Revision = "obsolete"
	if _, err := PlanOpenGaussRename(s, "renamed"); err == nil {
		t.Fatal("stale original accepted")
	}
	s = nativeRenameTestSnapshot(t, "original")
	for _, name := range []string{"original", "", "bad\x00name", strings.Repeat("表", 22)} {
		if _, err := PlanOpenGaussRename(s, name); err == nil {
			t.Fatal("invalid destination accepted", name)
		}
	}
}

func TestOpenGaussRenameArrayNamesClipUTF8AndRetainNativeSuffixes(t *testing.T) {
	occupied := map[string]bool{"_renamed": true, "_renamed_1": true}
	if name, err := OpenGaussRenameArrayName("renamed", occupied); err != nil || name != "_renamed_2" {
		t.Fatal(name, err)
	}
	destination := strings.Repeat("表", 21)
	first, err := OpenGaussRenameArrayName(destination, nil)
	if err != nil || first != "_"+strings.Repeat("表", 20) {
		t.Fatal(first, err)
	}
	name, err := OpenGaussRenameArrayName(destination, map[string]bool{first: true})
	if err != nil || name != first+"_1" || len(name) > 63 || !utf8.ValidString(name) {
		t.Fatal(name, err)
	}
}

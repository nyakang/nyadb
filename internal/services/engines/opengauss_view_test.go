package engines

import (
	"encoding/json"
	"testing"
)

func TestOpenGaussViewPreservationChecksNativeCatalogs(t *testing.T) {
	base := openGaussViewRecord{
		DatabaseOID: 1, NamespaceOID: 2, RoleOID: 3, RelationOID: 4,
		Namespace:  json.RawMessage(`{"nspname":"original","nspowner":3}`),
		Relation:   json.RawMessage(`{"relname":"v","relowner":3,"relacl":["reader=r/owner"],"relnatts":1,"reloptions":["security_barrier=true"]}`),
		Columns:    []json.RawMessage{json.RawMessage(`{"attname":"id","atttypid":20,"attacl":["reader=r/owner"]}`)},
		Types:      []json.RawMessage{json.RawMessage(`{"oid":5,"data":{"typname":"v","typowner":3}}`)},
		Rules:      []json.RawMessage{json.RawMessage(`{"oid":6,"data":{"rulename":"_RETURN","ev_action":"original"}}`)},
		Objects:    []json.RawMessage{json.RawMessage(`{"object_oid":4,"creator":3,"ctime":"created","mtime":"before","createcsn":7,"changecsn":8,"valid":true}`)},
		Comments:   []json.RawMessage{json.RawMessage(`{"description":"retained"}`)},
		Triggers:   []json.RawMessage{json.RawMessage(`{"oid":9,"data":{"tgname":"retained"}}`)},
		Incoming:   []json.RawMessage{json.RawMessage(`{"objid":10,"refobjid":4}`)},
		Outgoing:   []json.RawMessage{json.RawMessage(`{"objid":6,"refobjid":11}`)},
		Definition: "SELECT id FROM source",
	}
	copyRecord := func() openGaussViewRecord {
		raw, _ := json.Marshal(base)
		var out openGaussViewRecord
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !openGaussViewPreserved(base, base, "alter") || !openGaussViewPreserved(base, base, "rename") {
		t.Fatal("identical native catalog rejected")
	}
	for _, test := range []struct {
		name   string
		change func(*openGaussViewRecord)
	}{
		{"acl", func(r *openGaussViewRecord) {
			r.Relation = json.RawMessage(`{"relname":"v","relowner":3,"relacl":null,"relnatts":1}`)
		}},
		{"owner", func(r *openGaussViewRecord) { r.RoleOID++ }},
		{"identity", func(r *openGaussViewRecord) { r.RelationOID++ }},
		{"namespace", func(r *openGaussViewRecord) { r.NamespaceOID++ }},
		{"comment", func(r *openGaussViewRecord) { r.Comments = nil }},
		{"trigger", func(r *openGaussViewRecord) { r.Triggers = nil }},
		{"type", func(r *openGaussViewRecord) {
			r.Types[0] = json.RawMessage(`{"oid":5,"data":{"typname":"v","typowner":12}}`)
		}},
		{"column", func(r *openGaussViewRecord) {
			r.Columns[0] = json.RawMessage(`{"attname":"id","atttypid":23,"attacl":null}`)
		}},
		{"dependency", func(r *openGaussViewRecord) { r.Incoming = nil }},
		{"creation", func(r *openGaussViewRecord) {
			r.Objects[0] = json.RawMessage(`{"object_oid":4,"creator":3,"ctime":"recreated","createcsn":7,"valid":true}`)
		}},
		{"rule_identity", func(r *openGaussViewRecord) {
			r.Rules[0] = json.RawMessage(`{"oid":12,"data":{"rulename":"_RETURN","ev_action":"changed"}}`)
		}},
		{"hook", func(r *openGaussViewRecord) { r.Hooks = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := copyRecord()
			test.change(&after)
			if openGaussViewPreserved(base, after, "alter") {
				t.Fatal("lost metadata accepted")
			}
		})
	}
	allowed := copyRecord()
	allowed.Relation = json.RawMessage(`{"relname":"v","relowner":3,"relacl":["reader=r/owner"],"relnatts":2,"reloptions":["security_barrier=false"]}`)
	allowed.Columns = append(allowed.Columns, json.RawMessage(`{"attname":"extra","atttypid":20}`))
	allowed.Rules[0] = json.RawMessage(`{"oid":6,"data":{"rulename":"_RETURN","ev_action":"changed"}}`)
	allowed.Objects[0] = json.RawMessage(`{"object_oid":4,"creator":3,"ctime":"created","mtime":"after","createcsn":7,"changecsn":99,"valid":true}`)
	if !openGaussViewPreserved(base, allowed, "alter") || openGaussViewPreserved(base, allowed, "rename") {
		t.Fatal("intended replace changes not restricted to alter")
	}
	operational := copyRecord()
	operational.Objects = allowed.Objects
	if openGaussViewFingerprint(base) != openGaussViewFingerprint(operational) {
		t.Fatal("same-owner lock operational fields changed fingerprint")
	}
	for _, invalid := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`invalid`)} {
		if openGaussWithout(invalid) != "" || openGaussCatalogArrayMatches([]json.RawMessage{invalid}, []json.RawMessage{invalid}) || openGaussCatalogArrayMatches([]json.RawMessage{json.RawMessage(`{"oid":1,"data":null}`)}, []json.RawMessage{json.RawMessage(`{"oid":1,"data":null}`)}) {
			t.Fatal("invalid catalog compared equal", string(invalid))
		}
	}
}

package consul

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func prefixDeleteResult(t *testing.T, keys ...string) contract.KVResult {
	t.Helper()
	r := contract.KVResult{Completed: true, HTTP: &contract.KVHTTPResult{Captured: true, EOF: true, Ended: true, Acknowledged: true, Succeeded: true}}
	for _, key := range keys {
		raw, _ := json.Marshal(dataTestEntry(key, []byte{0, 255}))
		e, err := decodeEntry(raw)
		if err != nil {
			t.Fatal(err)
		}
		r.HTTP.Entries = append(r.HTTP.Entries, neutralEntry(e))
	}
	return r
}

func TestDataKeyPrefixDeletionCompleteOriginalSetExactIdentityAndAtomicBounds(t *testing.T) {
	f := &dataFixture{}
	l := dataTestLease(t, dataTestSession(t, "scope/", f))
	readsBefore := f.reads.Load()
	r := prefixDeleteResult(t, "scope/sub//猫", "scope/submarine", "scope/sub")
	p, err := l.prepareKeyPrefixDeletion("scope/sub", r)
	if err != nil || len(p) != 3 {
		t.Fatal(err)
	}
	request, _, err := mutationRequest(p)
	if err != nil {
		t.Fatal(err)
	}
	var ops []TxnOperation
	if err = json.Unmarshal(request.Body, &ops); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 9 || p[0].before.Key != "scope/sub" || p[1].before.Key != "scope/sub//猫" || p[2].before.Key != "scope/submarine" {
		t.Fatal("original prefix was treated as directory or altered", len(ops))
	}
	for _, e := range p {
		if e.edit.Kind != "delete" || e.edit.Original == nil || e.observation.ModifyIndex == "" {
			t.Fatal("candidate lost original conditional observation")
		}
	}
	r.HTTP.Entries[0].Value[0] = 7
	if p[1].observation.ValueHash != dataHash([]byte{0, 255}) || *p[1].edit.Original.Fingerprints[1] != dataHash([]byte{0, 255}) {
		t.Fatal("captured original source proof followed mutated response bytes")
	}
	keys := make([]string, 21)
	for i := range keys {
		keys[i] = "scope/sub/" + string(rune('a'+i))
	}
	if _, err = l.prepareKeyPrefixDeletion("scope/sub", prefixDeleteResult(t, keys...)); err != nil {
		t.Fatal("21 candidates should fit one original txn", err)
	}
	keys = append(keys, "scope/sub/z")
	if _, err = l.prepareKeyPrefixDeletion("scope/sub", prefixDeleteResult(t, keys...)); !errors.Is(err, ErrBounds) {
		t.Fatal("oversized prefix split or admitted", err)
	}
	if f.reads.Load() != readsBefore {
		t.Fatal("pure prefix preparation made requests")
	}
}

func TestDataKeyPrefixDeletionRejectsRootForeignHiddenLockedAndIncompleteObservations(t *testing.T) {
	f := &dataFixture{}
	l := dataTestLease(t, dataTestSession(t, "scope/", f))
	readsBefore := f.reads.Load()
	for _, fault := range []string{"root", "foreign-prefix", "foreign-key", "acl", "locked", "incomplete", "unacknowledged", "empty"} {
		t.Run(fault, func(t *testing.T) {
			prefix := "scope/sub"
			r := prefixDeleteResult(t, "scope/sub/key")
			switch fault {
			case "root":
				prefix = ""
			case "foreign-prefix":
				prefix = "other/"
			case "foreign-key":
				r.HTTP.Entries[0].Key = "scope/foreign"
			case "acl":
				v := true
				r.HTTP.Metadata.ACLFiltered = &v
			case "locked":
				r.HTTP.Entries[0].Session = "00000000-0000-4000-8000-000000000001"
			case "incomplete":
				r.HTTP.EOF = false
			case "unacknowledged":
				r.HTTP.Acknowledged = false
			case "empty":
				r.HTTP.Entries = nil
			}
			if p, err := l.prepareKeyPrefixDeletion(prefix, r); err == nil || p != nil {
				t.Fatal("invalid original prefix observation admitted")
			}
		})
	}
	if f.reads.Load() != readsBefore {
		t.Fatal("rejection made I/O")
	}
}

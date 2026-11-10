package etcd

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func TestDataEditingRejectsTamperedPayloadsBeforeNativeIO(t *testing.T) {
	s := dataTestSession(t)
	doc, _ := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	l := doc.(*dataLease)
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	entry := contract.KVEntry{Key: []byte{0, 255, 1}, Value: []byte{0, 255, 2}, CreateRevision: 2, ModRevision: 3, Version: 2, Lease: 9007199254740993}
	for _, mode := range []bool{false, true} {
		for _, mutation := range []string{"column", "mode", "ref", "payload", "metadata", "extra", "typed-ref"} {
			t.Run(fmt.Sprint(mode)+"/"+mutation, func(t *testing.T) {
				snapshot := l.snapshot(entry, mode)
				switch mutation {
				case "column":
					snapshot.Columns[1] = "renamed"
				case "mode":
					snapshot.Mode = "other"
				case "ref":
					snapshot.Ref.Values[0][2]++
				case "metadata":
					snapshot.Metadata += "x"
				case "typed-ref":
					snapshot.Ref.TypedValues[0].Bytes[2]++
				case "extra":
					if mode {
						snapshot.Fingerprints = []*string{nil}
					} else {
						snapshot.Original = []contract.Cell{{Kind: contract.Text}}
					}
				case "payload":
					if mode {
						snapshot.Original[1].Bytes[0]++
					} else {
						*snapshot.Fingerprints[1] = "foreign"
					}
				}
				_, err := l.preview(context.Background(), 1, "kv", "keys", []contract.DataEdit{{Kind: "update", Original: snapshot, Values: map[string]contract.DataValue{"value": {Kind: contract.Binary, Bytes: []byte("new")}}}})
				if err == nil || opens.Load() != 0 {
					t.Fatal("tampered signed payload dispatched", err)
				}
			})
		}
	}
	for _, v := range []contract.DataValue{{Kind: contract.Text, Text: "guess"}, {Kind: contract.Binary, Null: true}, {Kind: contract.Binary, Default: true}, {Kind: contract.Binary, Text: "0xff"}, {Kind: contract.Binary, Bytes: make([]byte, contract.DataEditMaxBytes+1)}} {
		_, err := l.preview(context.Background(), 1, "kv", "keys", []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: entry.Key}, "value": v}}})
		if err == nil || opens.Load() != 0 {
			t.Fatal("unsupported value dispatched", err)
		}
	}
}

func TestDataEditingRejectsOverlappingKeysAndNativeBatchBoundsBeforeIO(t *testing.T) {
	s := dataTestSession(t)
	doc, _ := s.ReserveDataDocument(s.KVScope().Namespace.Object("keyspace", "keys"))
	l := doc.(*dataLease)
	a := contract.KVEntry{Key: []byte{0, 255, 1}, Value: []byte("a"), CreateRevision: 1, ModRevision: 2, Version: 2}
	b := a.Clone()
	b.Key[2] = 2
	update := contract.DataEdit{Kind: "update", Original: l.snapshot(a, false), Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: b.Key}}}
	insert := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: b.Key}}}
	for _, edits := range [][]contract.DataEdit{{update, insert}, {update, {Kind: "delete", Original: l.snapshot(b, true)}}, {insert, insert}, {{Kind: "delete", Original: l.snapshot(a, true)}, {Kind: "delete", Original: l.snapshot(a, false)}}} {
		if _, err := l.prepareDataEdits(edits); err == nil {
			t.Fatal("overlapping keys accepted")
		}
	}
	var batch []contract.DataEdit
	for i := 0; i < 19; i++ {
		e := a.Clone()
		e.Key = []byte{0, 255, byte(i)}
		batch = append(batch, contract.DataEdit{Kind: "update", Original: l.snapshot(e, false), Values: map[string]contract.DataValue{"lease": {Kind: contract.Integer, Text: "0"}}})
	}
	if _, err := l.prepareDataEdits(batch[:18]); err != nil {
		t.Fatal("126 native operations rejected", err)
	}
	if _, err := l.prepareDataEdits(batch); err == nil {
		t.Fatal("133 native operations accepted")
	}
}

func TestDataMaximumKeySuccessorRemainsInBoundedByteOrder(t *testing.T) {
	key := bytes.Repeat([]byte{255}, 4096)
	key[100] = 254
	next := dataAfterKey(key)
	if !validKey(next) || len(next) != 101 || bytes.Compare(next, key) <= 0 || next[100] != 255 {
		t.Fatal("invalid bounded successor")
	}
	if dataAfterKey(bytes.Repeat([]byte{255}, 4096)) != nil {
		t.Fatal("maximum byte key had successor")
	}
	key = []byte{0, 255}
	if !bytes.Equal(dataAfterKey(key), []byte{0, 255, 0}) {
		t.Fatal("short binary boundary lost trailing zero")
	}
}

func TestDataInsertPreviewIsPureOwnedAndRevokedByReplacementOrClose(t *testing.T) {
	s := dataTestSession(t)
	o := s.KVScope().Namespace.Object("keyspace", "keys")
	a, _ := s.ReserveDataDocument(o)
	b, _ := s.ReserveDataDocument(o)
	l, foreign := a.(*dataLease), b.(*dataLease)
	var opens atomic.Int32
	s.opener = func(context.Context, connections.Profile, string) (*Client, error) {
		opens.Add(1)
		return nil, ErrInvalid
	}
	edits := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"key": {Kind: contract.Binary, Bytes: []byte{0, 255, 1}}, "lease": {Kind: contract.Integer, Text: "9007199254740993"}}}}
	first, err := l.preview(context.Background(), 1, "kv", "keys", edits)
	if err != nil || opens.Load() != 0 {
		t.Fatal("insertion preview dispatched", err)
	}
	copy := first.Snapshot()
	copy.Edits[0].Values["key"].Bytes[0] = 9
	if first.Snapshot().Edits[0].Values["key"].Bytes[0] != 0 {
		t.Fatal("public snapshot mutated original insertion")
	}
	if _, err := foreign.submit(context.Background(), 1, "kv", "keys", first); err == nil || opens.Load() != 0 {
		t.Fatal("foreign document consumed insertion")
	}
	second, err := l.preview(context.Background(), 1, "kv", "keys", edits)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot().NativeJSON != "" {
		t.Fatal("new preview retained old authorization")
	}
	if _, err := l.submit(context.Background(), 1, "kv", "keys", first); err == nil || opens.Load() != 0 {
		t.Fatal("replaced preview dispatched")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if second.Snapshot().NativeJSON != "" {
		t.Fatal("close retained native insertion parameters")
	}
}

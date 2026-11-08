package meilisearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataWriteFixture struct {
	mu     sync.Mutex
	doc    string
	status string
	writes atomic.Int32
}

func mockDataWrite(t *testing.T) (*dataLease, *dataWriteFixture) {
	f := &dataWriteFixture{doc: `{"id":9007199254740993,"old":true}`, status: "enqueued"}
	server := sessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/indexes/owned":
			fmt.Fprint(w, mockIndex)
		case "/indexes/owned/documents/9007199254740993":
			if f.doc == "" {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"code":"document_not_found"}`)
			} else {
				fmt.Fprint(w, f.doc)
			}
		case "/indexes/owned/documents", "/indexes/owned/documents/delete-batch":
			if r.Method == "GET" {
				fmt.Fprintf(w, `{"results":[%s],"offset":0,"limit":1,"total":1}`, f.doc)
				return
			}
			f.writes.Add(1)
			body, _ := io.ReadAll(r.Body)
			kind := "documentAdditionOrUpdate"
			if strings.HasSuffix(r.URL.Path, "delete-batch") {
				f.doc = ""
				kind = "documentDeletion"
			} else {
				var docs []json.RawMessage
				_ = json.Unmarshal(body, &docs)
				f.doc = string(docs[0])
			}
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"taskUid":42,"indexUid":"owned","status":"enqueued","type":"%s","enqueuedAt":"2026-11-08T10:00:00Z"}`, kind)
		case "/tasks/42":
			kind := "documentAdditionOrUpdate"
			if f.doc == "" {
				kind = "documentDeletion"
			}
			fmt.Fprintf(w, `{"uid":42,"indexUid":"owned","status":"%s","type":"%s","enqueuedAt":"2026-11-08T10:00:00Z"}`, f.status, kind)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"code":"document_not_found"}`)
		}
	})
	return reserveData(t, testSession(t, connections.MeilisearchConfig{Endpoint: server.URL}, "")), f
}

func TestMeilisearchDataWriteAdmissionObservationReadbackAndReceiptIsolation(t *testing.T) {
	l, f := mockDataWrite(t)
	ctx := context.Background()
	p, err := l.page(ctx, l.object.ProfileID, "owned", "documents", dataOptions(l, 1))
	if err != nil {
		t.Fatal(err)
	}
	full, err := l.row(ctx, l.object.ProfileID, "owned", "documents", *p.RowRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"fingerprints", "values"} {
		snapshot := p.RowSnapshots[0]
		if mode == "values" {
			snapshot = full.Snapshot
		}
		tampered := cloneDataValue(t, snapshot)
		if mode == "values" {
			tampered.Original[1].Text = `{"id":9007199254740993,"forged":true}`
		} else {
			*tampered.Fingerprints[1] = "forged"
		}
		_, err := l.save(ctx, l.object.ProfileID, "owned", "documents", []contract.DataEdit{{Kind: "delete", Original: tampered}})
		if !errors.Is(err, ErrInvalid) || f.writes.Load() != 0 {
			t.Fatal("tampered actual snapshot accepted", err)
		}
	}
	edits := []contract.DataEdit{{Kind: "update", Original: full.Snapshot, Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":9007199254740993,"n":9007199254740993,"猫":"🐱"}`}}}}
	result, err := l.save(ctx, l.object.ProfileID, "owned", "documents", edits)
	if err != nil || result.Async == nil || result.Async.TaskID != "42" || result.CommitConfirmed || result.CommitUncertain || len(result.Outcomes) != 0 {
		t.Fatal(result, err)
	}
	edits[0].Values["document"] = contract.DataValue{Text: "mutated caller"}
	result.Async.Status = "mutated caller"
	forged := cloneDataValue(t, result)
	forged.Async.Token = "forged"
	if _, err = l.verifyWrite(ctx, l.object.ProfileID, "owned", "documents", forged); !errors.Is(err, ErrInvalid) {
		t.Fatal("forged receipt accepted", err)
	}
	if _, err = l.save(ctx, l.object.ProfileID, "owned", "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) || f.writes.Load() != 1 {
		t.Fatal("pending batch replayed", err)
	}
	v, err := l.verifyWrite(ctx, l.object.ProfileID, "owned", "documents", result)
	if err != nil || v.Status != "pending" || v.Recovered.Async.Status != "enqueued" {
		t.Fatal(v, err)
	}
	f.mu.Lock()
	f.status = "succeeded"
	f.mu.Unlock()
	v, err = l.verifyWrite(ctx, l.object.ProfileID, "owned", "documents", result)
	if err != nil || v.Status != "applied" || !v.Recovered.CommitConfirmed || !v.Recovered.RefreshRequired || len(v.Recovered.Outcomes) != 1 || !strings.Contains(v.Recovered.Outcomes[0].Previews[1].Text, "9007199254740993") {
		t.Fatal(v, err)
	}
	if f.writes.Load() != 1 {
		t.Fatal("observation wrote")
	}
	if _, err = l.verifyWrite(ctx, l.object.ProfileID, "owned", "documents", result); !errors.Is(err, ErrInvalid) {
		t.Fatal("completed receipt reused", err)
	}
}

func TestMeilisearchDataWriteRejectsUnsupportedBatchesIdentityChangesAndJSON(t *testing.T) {
	l, f := mockDataWrite(t)
	ctx := context.Background()
	p, err := l.page(ctx, l.object.ProfileID, "owned", "documents", dataOptions(l, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"id":"different"}`, `{"id":"9007199254740993"}`, `{"id":9007199254740993,"x":1,"x":2}`, `{"id":9007199254740993,"x":"\ud800"}`, `[]`, `{"id":1e0}`} {
		_, err = l.save(ctx, l.object.ProfileID, "owned", "documents", []contract.DataEdit{{Kind: "update", Original: p.RowSnapshots[0], Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: raw}}}})
		if err == nil || f.writes.Load() != 0 {
			t.Fatal("invalid source admitted", err)
		}
	}
	_, err = l.save(ctx, l.object.ProfileID, "owned", "documents", []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}, {Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"new"}`}}}})
	if !errors.Is(err, contract.ErrUnsupported) || f.writes.Load() != 0 {
		t.Fatal("mixed deletion claimed atomic batch", err)
	}
	f.mu.Lock()
	f.doc = `{"id":9007199254740993,"changed":true}`
	f.mu.Unlock()
	_, err = l.save(ctx, l.object.ProfileID, "owned", "documents", []contract.DataEdit{{Kind: "delete", Original: p.RowSnapshots[0]}})
	if !errors.Is(err, contract.ErrDataConflict) || f.writes.Load() != 0 {
		t.Fatal("conflicting original admitted", err)
	}
}

func TestMeilisearchDataWriteFailedOrConflictingReadbackNeverPermitsReplay(t *testing.T) {
	for _, mode := range []string{"failed", "canceled", "readback_conflict"} {
		t.Run(mode, func(t *testing.T) {
			l, f := mockDataWrite(t)
			ctx := context.Background()
			p, err := l.page(ctx, l.object.ProfileID, "owned", "documents", dataOptions(l, 1))
			if err != nil {
				t.Fatal(err)
			}
			edits := []contract.DataEdit{{Kind: "update", Original: p.RowSnapshots[0], Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":9007199254740993,"new":true}`}}}}
			result, err := l.save(ctx, l.object.ProfileID, "owned", "documents", edits)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.status = mode
			if mode == "readback_conflict" {
				f.status = "succeeded"
				f.doc = `{"id":9007199254740993,"concurrent":true}`
			}
			f.mu.Unlock()
			v, err := l.verifyWrite(ctx, l.object.ProfileID, "owned", "documents", result)
			if err != nil || v.Status != "indeterminate" || v.Recovered.Async.TaskJSON == "" {
				t.Fatal(v, err)
			}
			if _, err = l.save(ctx, l.object.ProfileID, "owned", "documents", edits); !errors.Is(err, contract.ErrDataCommitUncertain) || f.writes.Load() != 1 {
				t.Fatal("terminal evidence permitted replay", err)
			}
		})
	}
}

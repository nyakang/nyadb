package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func dataCopySnapshot(t *testing.T, l *dataLease, entry map[string]any, mode string) *contract.RowSnapshot {
	t.Helper()
	raw, _ := json.Marshal(entry)
	e, err := decodeEntry(raw)
	if err != nil {
		t.Fatal(err)
	}
	n := neutralEntry(e)
	s := l.snapshot(n, contract.KVHTTPMetadata{})
	if mode == "values" {
		s.Mode, s.Fingerprints = mode, nil
		for _, c := range dataCells(n, false) {
			s.Original = append(s.Original, c.Value.Clone())
		}
	}
	return s
}

func TestDataKeyCopyOriginalSourceBothPayloadsDistinctDestinationAndPrivateBoundary(t *testing.T) {
	f := &dataFixture{}
	l := dataTestLease(t, dataTestSession(t, "scope/", f))
	before := f.reads.Load()
	for _, mode := range []string{"values", "fingerprints"} {
		s := dataCopySnapshot(t, l, dataTestEntry("scope/key", []byte{0, 255}), mode)
		r := contract.DataKeyCopy{Source: s, Destination: "scope/copied//"}
		p, err := l.prepareKeyCopy(r)
		if err != nil || len(p) != 1 || p[0].after.Flags != math.MaxUint64 || !p[0].copy {
			t.Fatal("original copy preparation failed", err)
		}
		r.Source.Ref.Values[0][0] = '!'
		if p[0].edit.Original.Ref.Values[0][0] == '!' {
			t.Fatal("copy plan shares caller source bytes")
		}
	}
	if f.reads.Load() != before || l.Runtime().PreviewKeyCopy != nil {
		t.Fatal("private copy validation made I/O or published prematurely")
	}
	if _, err := l.prepareDataEdits([]contract.DataEdit{{Kind: "copy", Original: dataCopySnapshot(t, l, dataTestEntry("scope/key", nil), "fingerprints"), Values: map[string]contract.DataValue{"key": dataText("scope/copied")}}}); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("ordinary editor inferred copy semantics", err)
	}
}

func TestDataKeyCopyMalformedForeignLockedSameAndOutOfPrefixRejectBeforeIO(t *testing.T) {
	f := &dataFixture{}
	l := dataTestLease(t, dataTestSession(t, "scope/", f))
	before := f.reads.Load()
	for _, mode := range []string{"values", "fingerprints"} {
		for _, destination := range []string{"", "scope/key", "foreign/key", "scope/key\x00", "scope/" + strings.Repeat("x", 4097)} {
			if _, err := l.previewKeyCopy(context.Background(), 11, "kv", "keys", contract.DataKeyCopy{Source: dataCopySnapshot(t, l, dataTestEntry("scope/key", nil), mode), Destination: destination}); err == nil {
				t.Fatal("invalid destination authorized copy", mode, destination)
			}
		}
		s := dataCopySnapshot(t, l, dataTestEntry("scope/key", nil), mode)
		s.Ref.Object.ProfileID++
		if _, err := l.previewKeyCopy(context.Background(), 11, "kv", "keys", contract.DataKeyCopy{Source: s, Destination: "scope/copied"}); err == nil {
			t.Fatal("foreign original accepted")
		}
		s = dataCopySnapshot(t, l, dataTestEntry("scope/key", nil), mode)
		s.Metadata = "{}"
		if _, err := l.previewKeyCopy(context.Background(), 11, "kv", "keys", contract.DataKeyCopy{Source: s, Destination: "scope/copied"}); err == nil {
			t.Fatal("corrupt signed original accepted")
		}
		entry := dataTestEntry("scope/key", nil)
		entry["Session"] = testAgentID
		s = dataCopySnapshot(t, l, entry, mode)
		if _, err := l.previewKeyCopy(context.Background(), 11, "kv", "keys", contract.DataKeyCopy{Source: s, Destination: "scope/copied"}); !errors.Is(err, contract.ErrUnsupported) {
			t.Fatal("original locked source accepted", err)
		}
	}
	if f.reads.Load() != before {
		t.Fatal("invalid copy made native I/O")
	}
}

func TestDataKeyCopyCompleteOriginalReceiptsRequireUnchangedSourceAndSavedDestination(t *testing.T) {
	for _, fault := range []string{"none", "missing-source", "changed-source", "wrong-source-index", "wrong-target-value", "extra-result"} {
		t.Run(fault, func(t *testing.T) {
			f := &dataFixture{}
			value := bytes.Repeat([]byte{0, 255}, valueLimit/2)
			source := dataTestEntry("scope/key", value)
			source["CreateIndex"], source["ModifyIndex"] = uint64(40), uint64(41)
			f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/v1/txn" {
					return false
				}
				var ops []TxnOperation
				if json.NewDecoder(r.Body).Decode(&ops) != nil {
					t.Error("original copy request missing")
					return true
				}
				if len(ops) == 1 {
					w.Write([]byte(`{"Results":null,"Errors":null}`))
					return true
				}
				if len(ops) == 2 {
					json.NewEncoder(w).Encode(map[string]any{"Results": []any{map[string]any{"KV": source}}, "Errors": []any{}})
					return true
				}
				verbs := []string{}
				for _, op := range ops {
					verbs = append(verbs, op.KV.Verb)
				}
				if !reflect.DeepEqual(verbs, []string{"check-index", "check-not-exists", "cas", "get", "get"}) || !bytes.Equal(*ops[2].KV.Value, value) || *ops[2].KV.Flags != math.MaxUint64 || *ops[2].KV.Index != 0 {
					t.Error("native copy changed original value/Flags/conditions", verbs)
				}
				check := map[string]any{}
				for k, v := range source {
					check[k] = v
				}
				check["Value"] = nil
				target := map[string]any{"Key": "scope/copied//", "Value": value, "Flags": uint64(math.MaxUint64), "CreateIndex": uint64(42), "ModifyIndex": uint64(42), "LockIndex": uint64(0)}
				projected := map[string]any{}
				for k, v := range target {
					projected[k] = v
				}
				projected["Value"] = nil
				original := map[string]any{}
				for k, v := range source {
					original[k] = v
				}
				switch fault {
				case "changed-source":
					original["Value"] = []byte{7}
				case "wrong-source-index":
					original["ModifyIndex"] = uint64(43)
				case "wrong-target-value":
					target["Value"] = []byte{7}
				}
				results := []any{map[string]any{"KV": check}, map[string]any{"KV": projected}, map[string]any{"KV": original}, map[string]any{"KV": target}}
				if fault == "missing-source" {
					results = append(results[:2], results[3])
				}
				if fault == "extra-result" {
					results = append(results, map[string]any{"KV": target})
				}
				json.NewEncoder(w).Encode(map[string]any{"Results": results, "Errors": []any{}})
				return true
			}
			l := dataTestLease(t, dataTestSession(t, "scope/", f))
			req := contract.DataKeyCopy{Source: dataCopySnapshot(t, l, source, "values"), Destination: "scope/copied//"}
			p, err := l.previewKeyCopy(context.Background(), 11, "kv", "keys", req)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			req.Source.Original[1].Bytes[0] = 17
			out, err := l.submit(context.Background(), 11, "kv", "keys", p)
			if fault == "none" {
				if err != nil || out == nil || !out.CommitConfirmed || out.CommitUncertain || out.Applied != 1 || out.Outcomes[0].Kind != "copy" || out.Outcomes[0].Original.Original[1].Bytes[0] != 0 || out.Outcomes[0].Saved == nil {
					t.Fatal("complete original copy not established", err)
				}
			} else {
				if !errors.Is(err, contract.ErrDataCommitUncertain) || out == nil || out.CommitConfirmed || !(out.CommitUncertain || out.Incomplete) {
					t.Fatal("incomplete copy correspondence promoted success", err)
				}
				before := f.reads.Load()
				if _, err := l.previewKeyCopy(context.Background(), 11, "kv", "keys", req); !errors.Is(err, contract.ErrDataCommitUncertain) || f.reads.Load() != before {
					t.Fatal("unknown copy replay barrier lost", err)
				}
			}
		})
	}
}

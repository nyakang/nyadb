package solr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const editSchema = `{"uniqueKey":"id","fields":[{"name":"id","type":"string"}],"fieldTypes":[{"name":"string","class":"solr.StrField"},{"name":"plong","class":"solr.LongPointField"}]}`
const editConfig = `{"requestHandler":{"/select":{"class":"solr.SearchHandler"},"/get":{"class":"solr.RealTimeGetHandler","useParams":"_GET","defaults":{"omitHeader":true}},"/update":{"class":"solr.UpdateRequestHandler","useParams":"_UPDATE"}},"updateRequestProcessorChain":[{"name":"verified","": [{"class":"solr.LogUpdateProcessorFactory"},{"class":"solr.DistributedUpdateProcessorFactory"},{"class":"solr.RunUpdateProcessorFactory"}]}]}`
const editFields = `{"responseHeader":{"status":0},"fields":[{"name":"id","type":"string","stored":true,"indexed":true,"docValues":false,"multiValued":false,"required":true},{"name":"text_s","type":"string","stored":true,"indexed":true,"docValues":false,"multiValued":false,"required":false},{"name":"exact_l","type":"plong","stored":true,"indexed":true,"docValues":true,"multiValued":false,"required":false},{"name":"_version_","type":"plong","stored":false,"indexed":false,"docValues":true,"multiValued":false,"required":false}]}`

func TestSolrWriteQualificationRejectsLossySchemaAndCustomChains(t *testing.T) {
	observations := []json.RawMessage{[]byte(strings.Replace(editFields, `"responseHeader":{"status":0},`, "", 1)), []byte(`{"dynamicFields":[]}`), []byte(`{"response":{"znodeVersion":-1}}`)}
	m := MemberMetadata{Schema: []byte(editSchema), Config: []byte(editConfig)}
	if _, err := qualifyWriteMember(m, observations); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"index_only", "missing_default", "nested", "dynamic_loss", "stored_copy", "params", "custom_chain", "custom_handler", "invariant"} {
		t.Run(change, func(t *testing.T) {
			values := append([]json.RawMessage(nil), observations...)
			member := m
			switch change {
			case "index_only":
				values[0] = []byte(strings.Replace(string(values[0]), `"name":"text_s","type":"string","stored":true`, `"name":"text_s","type":"string","stored":false`, 1))
			case "missing_default":
				values[0] = []byte(strings.Replace(string(values[0]), `"stored":true,`, "", 1))
			case "nested":
				values[0] = []byte(strings.Replace(string(values[0]), "text_s", "_root_", 1))
			case "dynamic_loss":
				values[1] = []byte(`{"dynamicFields":[{"name":"*_hidden","type":"string","stored":false,"indexed":true,"docValues":false,"multiValued":false,"required":false}]}`)
			case "stored_copy":
				member.Schema = []byte(strings.Replace(editSchema, `"fields":`, `"copyFields":[{"source":"id","dest":"text_s"}],"fields":`, 1))
			case "params":
				values[2] = []byte(`{"response":{"znodeVersion":1,"params":{"_UPDATE":{"update.chain":"custom"}}}}`)
			case "custom_chain":
				member.Config = []byte(strings.Replace(editConfig, "LogUpdateProcessorFactory", "CustomProcessor", 1))
			case "custom_handler":
				member.Config = []byte(strings.Replace(editConfig, "RealTimeGetHandler", "CustomGetHandler", 1))
			case "invariant":
				member.Config = []byte(strings.Replace(editConfig, `"useParams":"_UPDATE"`, `"invariants":{"_route_":"other"}`, 1))
			}
			if _, err := qualifyWriteMember(member, values); !errors.Is(err, contract.ErrUnsupported) {
				t.Fatal("unsafe target qualified", err)
			}
		})
	}
}
func TestSolrSharedSaveBothSnapshotsConflictPartialAndNoReplay(t *testing.T) {
	for _, mode := range []string{"fingerprints", "values", "lost", "partial", "conflict", "params", "numeric_id"} {
		t.Run(mode, func(t *testing.T) {
			m := newDataMock()
			m.schema = editSchema
			m.config = editConfig
			m.put("a", 100, "before")
			m.put("b", 100, "before")
			var writes atomic.Int32
			var params atomic.Bool
			serve := func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/solr/owned/schema/fields":
					fmt.Fprint(w, editFields)
					return
				case "/solr/owned/config/params":
					if params.Load() {
						fmt.Fprint(w, `{"responseHeader":{"status":0},"response":{"znodeVersion":1,"params":{"_UPDATE":{"_route_":"foreign"}}}}`)
					} else {
						fmt.Fprint(w, `{"responseHeader":{"status":0},"response":{"znodeVersion":-1}}`)
					}
					return
				case "/solr/owned/get":
					m.mu.Lock()
					raw := m.documents[r.URL.Query().Get("id")]
					m.mu.Unlock()
					if len(raw) == 0 {
						raw = []byte("null")
					}
					fmt.Fprint(w, `{"responseHeader":{"status":0},"doc":`+string(raw)+`}`)
					return
				case "/solr/owned/update":
					if r.URL.Query().Get("update.chain") != "verified" {
						t.Error("unbound update chain")
					}
					writes.Add(1)
					if mode == "partial" && writes.Load() == 2 {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"error":{"code":409}}`)
						return
					}
					raw, _ := io.ReadAll(r.Body)
					var docs []map[string]json.RawMessage
					if decodeJSON(raw, &docs) != nil || len(docs) != 1 {
						t.Error("mutation format")
						return
					}
					var id string
					_ = decodeJSON(docs[0]["id"], &id)
					var set struct{ Set json.RawMessage }
					_ = decodeJSON(docs[0]["text_s"], &set)
					var text string
					_ = decodeJSON(set.Set, &text)
					m.put(id, 100+int64(writes.Load()), text)
					if mode == "lost" {
						h := w.(http.Hijacker)
						c, _, _ := h.Hijack()
						_ = c.Close()
						return
					}
					fmt.Fprintf(w, `{"responseHeader":{"status":0},"adds":[%q,%d]}`, id, 100+writes.Load())
					return
				}
				m.serve(w, r)
			}
			server := httptest.NewServer(http.HandlerFunc(serve))
			defer server.Close()
			s := testSession(t, connections.SolrConfig{Endpoint: server.URL}, "")
			lease := nativeData(t, s, "core", "owned")
			runtime := lease.Runtime()
			ctx := context.Background()
			page, err := runtime.AdvancedLoader(ctx, 71, s.catalog, "owned", contract.DataOptions{Object: lease.object, Limit: 2})
			if err != nil || page.Editable == nil || !*page.Editable || page.ColumnMetadata[4].Generated {
				t.Fatal("qualified shared page", err)
			}
			original := page.RowSnapshots[0]
			if mode == "values" {
				row, e := runtime.RowReader(ctx, 71, s.catalog, "owned", original.Ref)
				if e != nil {
					t.Fatal(e)
				}
				original = row.Snapshot
			}
			desired := `{"id":"a","_version_":100,"exact_l":9007199254740993,"text_s":"after"}`
			edits := []contract.DataEdit{{Kind: "update", Original: original, Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: desired}}}}
			if mode == "partial" {
				edits = append(edits, contract.DataEdit{Kind: "update", Original: page.RowSnapshots[1], Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: strings.Replace(desired, `"a"`, `"b"`, 1)}}})
			}
			if mode == "conflict" {
				m.put("a", 200, "other")
			}
			if mode == "params" {
				params.Store(true)
			}
			if mode == "numeric_id" {
				edits = []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":9,"text_s":"new"}`}}}}
			}
			result, err := runtime.Saver(ctx, 71, s.catalog, "owned", edits)
			if mode == "conflict" || mode == "params" || mode == "numeric_id" {
				if err == nil || writes.Load() != 0 {
					t.Fatal("precondition dispatched", err)
				}
				return
			}
			if mode == "lost" || mode == "partial" {
				if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain {
					t.Fatal(result, err)
				}
				count := writes.Load()
				if _, e := runtime.Saver(ctx, 71, s.catalog, "owned", edits); !errors.Is(e, contract.ErrDataCommitUncertain) || count != writes.Load() {
					t.Fatal("uncertain replayed", e)
				}
				verification, e := runtime.Verify(ctx, 71, s.catalog, "owned", result)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "lost" {
					if verification.Status != "applied" || !verification.Recovered.Verified {
						t.Fatal(verification)
					}
				} else if verification.Status != "indeterminate" || verification.States[0] != "applied" || verification.States[1] != "not_applied" {
					t.Fatal(verification)
				}
				if count != writes.Load() {
					t.Fatal("verify replayed")
				}
			} else if err != nil || !result.CommitConfirmed || result.Applied != 1 || result.Outcomes[0].Saved == nil {
				t.Fatal(result, err)
			}
		})
	}
}
func TestSolrEditSnapshotAndDeltaProtectIdentityCopyAndTypedTokens(t *testing.T) {
	m := newDataMock()
	s, raw, r := mockDataDocument(t, m)
	m.put("a", 100, "before")
	page, err := r.AdvancedLoader(context.Background(), 71, s.catalog, "owned", contract.DataOptions{Object: s.Namespace("").Object("core", "owned"), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	lease := raw.(*dataLease)
	for _, mode := range []string{"fingerprint", "column", "metadata", "version"} {
		bad := cloneSolrSnapshot(page.RowSnapshots[0])
		switch mode {
		case "fingerprint":
			*bad.Fingerprints[4] = "forged"
		case "column":
			bad.Columns[4] = "other"
		case "metadata":
			bad.Metadata = "forged"
		case "version":
			bad.Ref.TypedValues[0].Text = "other"
		}
		if _, e := lease.validateEditSnapshot(s.catalog, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal("forged snapshot", mode, e)
		}
	}
	member := &writeMember{fields: map[string]effectiveField{}, copies: map[string]bool{"dest": true}}
	original := map[string]json.RawMessage{"id": []byte(`"a"`), "dest": []byte(`"copy"`)}
	desired := map[string]json.RawMessage{"id": []byte(`"b"`), "dest": []byte(`"copy"`)}
	if _, err := qualifyDesired(member, "id", desired, original, false); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("key mutated", err)
	}
	if _, err := qualifyDesired(member, "id", original, original, true); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("copy input created", err)
	}
}

func TestSolrExactEmptyNativeFacetMayOmitDuplicateChild(t *testing.T) {
	for _, fixture := range []struct {
		total, count int
		docs         string
		valid        bool
	}{
		{0, 0, `[]`, true}, {1, 1, `[]`, false}, {0, 1, `[]`, false}, {0, 0, `[{"id":"a","_version_":100}]`, false},
	} {
		m := newDataMock()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/select") {
				fmt.Fprintf(w, `{"responseHeader":{"status":0},"response":{"numFound":%d,"numFoundExact":true,"start":0,"docs":%s},"facets":{"count":%d}}`, fixture.total, fixture.docs, fixture.count)
				return
			}
			m.serve(w, r)
		}))
		client, err := Open(context.Background(), connections.SolrConfig{Endpoint: server.URL}, "")
		if err != nil {
			t.Fatal(err)
		}
		page, err := client.selectProjection(context.Background(), "owned", readUnit{Member: "owned", Key: "id"}, nil, "", 2)
		client.Close()
		server.Close()
		if fixture.valid {
			if err != nil || page.Total != 0 || len(page.Documents) != 0 {
				t.Fatal("exact native empty facet rejected", page, err)
			}
		} else if !errors.Is(err, ErrProtocol) {
			t.Fatal("incomplete or contradictory facet accepted", fixture, err)
		}
	}
}

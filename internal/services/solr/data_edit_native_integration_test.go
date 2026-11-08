package solr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func editNativePage(t *testing.T, ctx context.Context, l *dataLease, key string) *contract.DataPage {
	t.Helper()
	o := contract.DataOptions{Object: l.object, Limit: 500}
	if key != "" {
		o.FilterColumn = "_key"
		o.FilterMode = "equal"
		o.FilterValue = key
	}
	page, err := l.Runtime().AdvancedLoader(ctx, l.object.ProfileID, l.object.Catalog, l.object.Name, o)
	if err != nil || page.Editable == nil || !*page.Editable {
		t.Fatal("native write-qualified page", page, err)
	}
	return page
}
func editJSON(t *testing.T, s *contract.RowSnapshot, document string) contract.DataEdit {
	t.Helper()
	return contract.DataEdit{Kind: "update", Original: s, Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: document}}}
}
func TestSolrNativeSharedEditsQualifiedCRUDSnapshotsAndRevocation(t *testing.T) {
	if os.Getenv("NYADB_SOLR_EDIT_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned qualified editing fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 480*time.Second)
	defer cancel()
	cfg, secret := readNativeFixture(t)
	admin := documentNativeOpen(t, ctx)
	session := testSession(t, cfg, secret)
	for _, mode := range []string{"fingerprints", "values"} {
		t.Run(mode, func(t *testing.T) {
			key := "edit-" + mode
			nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_edit/update?commit=true&wt=json", Body: []byte(fmt.Sprintf(`[{"id":%q,"exact_l":9007199254740993,"text_s":"before","tags":["b","a","b"]}]`, key))})
			lease := nativeData(t, session, "core", "nyadb_edit")
			page := editNativePage(t, ctx, lease, key)
			snapshot := page.RowSnapshots[0]
			text := page.Rows[0][4].Value.Text
			if mode == "values" {
				row, err := lease.Runtime().RowReader(ctx, session.ProfileID(), session.catalog, "nyadb_edit", snapshot.Ref)
				if err != nil {
					t.Fatal(err)
				}
				snapshot = row.Snapshot
				text = row.Values["document"].Text
			}
			desired := strings.Replace(text, `"before"`, `"after🐱"`, 1)
			result, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", []contract.DataEdit{editJSON(t, snapshot, desired)})
			if err != nil || !result.CommitConfirmed || result.Applied != 1 || result.Outcomes[0].Saved == nil {
				t.Fatal("native original atomic set", result, err)
			}
			var saved map[string]json.RawMessage
			_ = decodeJSON([]byte(result.Outcomes[0].Previews[4].Value.Text), &saved)
			if string(saved["exact_l"]) != "9007199254740993" || !jsonEqual(saved["tags"], []byte(`["b","a","b"]`)) {
				t.Fatal("native exact/ordered untouched values changed", saved)
			}
			insert := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: fmt.Sprintf(`{"id":%q,"text_s":"created"}`, key+"-created")}}}
			created, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", []contract.DataEdit{insert})
			if err != nil || !created.CommitConfirmed {
				t.Fatal("native insert", created, err)
			}
			if _, err = lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", []contract.DataEdit{insert}); !errors.Is(err, contract.ErrDataConflict) {
				t.Fatal("native duplicate insert", err)
			}
			nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_edit/update?commit=true&wt=json", Body: []byte(`{"commit":{}}`)})
			createdPage := editNativePage(t, ctx, lease, key+"-created")
			deleted, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", []contract.DataEdit{{Kind: "delete", Original: createdPage.RowSnapshots[0]}})
			if err != nil || !deleted.CommitConfirmed || deleted.Applied != 1 {
				t.Fatal("native delete", deleted, err)
			}
		})
	}
	lease := nativeData(t, session, "core", "nyadb_edit")
	page := editNativePage(t, ctx, lease, "ui-edit")
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_edit/config/params?wt=json", Body: []byte(`{"set":{"_UPDATE":{"_route_":"foreign"}}}`)})
	if _, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", []contract.DataEdit{editJSON(t, page.RowSnapshots[0], strings.Replace(page.Rows[0][4].Value.Text, `"before"`, `"changed"`, 1))}); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("separate params not revoked", err)
	}
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_edit/config/params?wt=json", Body: []byte(`{"delete":"_UPDATE"}`)})
	if _, err := lease.Runtime().AdvancedLoader(ctx, session.ProfileID(), session.catalog, "nyadb_edit", contract.DataOptions{Object: lease.object, Limit: 1}); !errors.Is(err, ErrDataTargetChanged) {
		t.Fatal("restored store silently rebound", err)
	}
}
func TestSolrNativeCloudSharedEditsNaturalRoutingAndExplicitMembers(t *testing.T) {
	if os.Getenv("NYADB_SOLR_EDIT_IT") != "1" || os.Getenv("NYADB_SOLR_CLOUD_IT") != "1" {
		t.Skip("requires owned qualified Cloud fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1080*time.Second)
	defer cancel()
	cfg, secret := readNativeFixture(t)
	admin := documentNativeOpen(t, ctx)
	var collections []string
	aliasCreated := false
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		if aliasCreated {
			if _, err := admin.Do(cleanupCtx, Request{Method: "GET", Path: "/admin/collections?action=DELETEALIAS&name=nyadb_edit_alias&wt=json"}); err != nil {
				t.Errorf("remove owned editing alias: %v", err)
			}
		}
		for i := len(collections) - 1; i >= 0; i-- {
			if _, err := admin.Do(cleanupCtx, Request{Method: "GET", Path: "/admin/collections?action=DELETE&name=" + url.QueryEscape(collections[i]) + "&wt=json"}); err != nil {
				t.Errorf("remove owned editing collection %s: %v", collections[i], err)
			}
		}
	})
	session := testSession(t, cfg, secret)
	for _, router := range []string{"implicit", "compositeId"} {
		for _, routeField := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/field_%t", router, routeField), func(t *testing.T) {
				name := fmt.Sprintf("nyadb_edit_%s_%t", router, routeField)
				p := url.Values{"action": {"CREATE"}, "name": {name}, "collection.configName": {"nyadb_edit"}, "replicationFactor": {"1"}, "wt": {"json"}, "router.name": {router}}
				if router == "implicit" {
					p.Set("shards", "s1,s2")
				} else {
					p.Set("numShards", "4")
				}
				if routeField {
					p.Set("router.field", "route_s")
				}
				nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/collections?" + p.Encode()})
				collections = append(collections, name)
				keys := []string{"plain🐱", "a!", "a!!", "a!b!", "a!b!!", "app/2!user/4!id!tail", "app/0!user/32!x", "app/32!suffix", "a/!b", "!", "!!", "a!b!c!d"}
				docs := make([]map[string]any, 0, len(keys))
				for _, key := range keys {
					doc := map[string]any{"id": key, "text_s": "before"}
					if routeField {
						if router == "implicit" {
							doc["route_s"] = "s1"
						} else {
							doc["route_s"] = key
						}
					}
					docs = append(docs, doc)
				}
				raw, _ := json.Marshal(docs)
				path := "/" + name + "/update?commit=true&wt=json"
				if router == "implicit" {
					path += "&_route_=s1"
				}
				nativeRun(t, admin, ctx, Request{Method: "POST", Path: path, Body: raw})
				if router == "implicit" {
					doc := map[string]any{"id": keys[0], "text_s": "sibling"}
					if routeField {
						doc["route_s"] = "s2"
					}
					raw, _ := json.Marshal([]any{doc})
					nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/" + name + "/update?commit=true&wt=json&_route_=s2", Body: raw})
				}
				lease := nativeData(t, session, "collection", name)
				page := editNativePage(t, ctx, lease, "")
				m := lease.writeBinding.members[name]
				for i, snapshot := range page.RowSnapshots {
					identity, err := lease.ref(session.catalog, snapshot.Ref)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					_ = decodeJSON([]byte(page.Rows[i][4].Value.Text), &fields)
					ref, err := m.nativeRef(identity.Unit, identity.Key, fields)
					if err != nil {
						t.Fatal("native indexed shard differs from exact native hash", identity.Unit, identity.Key, err)
					}
					if ref.Router == "compositeId" {
						wrong := identity.Unit
						for shard := range m.ranges {
							if shard != wrong.Shard {
								wrong.Shard = shard
								break
							}
						}
						if _, e := m.nativeRef(wrong, identity.Key, fields); !errors.Is(e, contract.ErrDataConflict) {
							t.Fatal("observed nonnatural shard was writable", e)
						}
					}
					if string(fields["text_s"]) == `"sibling"` {
						continue
					}
					if identity.Key.Text != keys[0] && identity.Key.Text != "app/2!user/4!id!tail" {
						continue
					}
					desired := strings.Replace(page.Rows[i][4].Value.Text, `"before"`, `"after"`, 1)
					result, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, name, []contract.DataEdit{editJSON(t, snapshot, desired)})
					if err != nil || !result.CommitConfirmed {
						t.Fatal("native qualified Cloud atomic set", result, err)
					}
				}
				insert := map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"new!insert","text_s":"created"}`}}
				if router == "implicit" {
					insert["_shard"] = contract.DataValue{Kind: contract.Text, Text: "s1"}
				}
				if routeField {
					insert["document"] = contract.DataValue{Kind: contract.JSON, Text: `{"id":"new!insert","text_s":"created","route_s":"new!route"}`}
					if router == "implicit" {
						insert["document"] = contract.DataValue{Kind: contract.JSON, Text: `{"id":"new!insert","text_s":"created","route_s":"s1"}`}
					}
				}
				if router == "compositeId" && routeField {
					duplicate := []contract.DataEdit{{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"duplicate-route","route_s":9,"text_s":"one"}`}}}, {Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"duplicate-route","route_s":"9","text_s":"two"}`}}}}
					if r, e := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, name, duplicate); !errors.Is(e, ErrInvalid) || r != nil {
						t.Fatal("duplicate physical key was dispatched", r, e)
					}
				}
				result, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, name, []contract.DataEdit{{Kind: "insert", Values: insert}})
				if err != nil || !result.CommitConfirmed {
					t.Fatal("native explicit/natural insert", result, err)
				}
				if router == "implicit" {
					siblingRef := NativeDocumentRef{Member: name, UniqueKey: "id", Key: contract.Cell{Kind: contract.Text, Text: keys[0]}, Router: router, Shard: "s2"}
					if routeField {
						siblingRef.RouteField = "route_s"
						siblingRef.RouteValue = contract.Cell{Kind: contract.Text, Text: "s2"}
					}
					sibling, err := admin.ReadNativeDocument(ctx, siblingRef)
					if err != nil || !strings.Contains(string(sibling.Projection), `"sibling"`) {
						t.Fatal("same-key sibling changed", err)
					}
				}
			})
		}
	}
	nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/collections?action=CREATEALIAS&name=nyadb_edit_alias&collections=nyadb_edit_compositeId_false,nyadb_edit_compositeId_true&wt=json"})
	aliasCreated = true
	alias := nativeData(t, session, "alias", "nyadb_edit_alias")
	_ = editNativePage(t, ctx, alias, "")
	insert := contract.DataEdit{Kind: "insert", Values: map[string]contract.DataValue{"document": {Kind: contract.JSON, Text: `{"id":"alias-new","text_s":"created"}`}}}
	if _, err := alias.Runtime().Saver(ctx, session.ProfileID(), session.catalog, alias.object.Name, []contract.DataEdit{insert}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous alias member selected", err)
	}
	insert.Values["_member"] = contract.DataValue{Kind: contract.Text, Text: "nyadb_edit_compositeId_false"}
	result, err := alias.Runtime().Saver(ctx, session.ProfileID(), session.catalog, alias.object.Name, []contract.DataEdit{insert})
	if err != nil || !result.CommitConfirmed {
		t.Fatal("explicit alias member insert", result, err)
	}
}

func TestSolrNativeSharedEditsLostResponseVerificationWithoutReplay(t *testing.T) {
	if os.Getenv("NYADB_SOLR_EDIT_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned qualified mutation fault fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin := nativeOpen(t, ctx)
	nativeRun(t, admin, ctx, Request{Method: "POST", Path: "/nyadb_edit/update?commit=true&wt=json", Body: []byte(`[{"id":"lost-shared","text_s":"before"}]`)})
	var writes atomic.Int32
	relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/update") {
			writes.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = connection.Close()
			}
			return true
		}
		return false
	})
	cfg, secret := nativeFixture(t)
	cfg = relay.config
	secret = ""
	session := testSession(t, cfg, secret)
	lease := nativeData(t, session, "core", "nyadb_edit")
	page := editNativePage(t, ctx, lease, "lost-shared")
	edits := []contract.DataEdit{editJSON(t, page.RowSnapshots[0], strings.Replace(page.Rows[0][4].Value.Text, `"before"`, `"after-lost"`, 1))}
	result, err := lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", edits)
	if !errors.Is(err, contract.ErrDataCommitUncertain) || result == nil || !result.CommitUncertain || writes.Load() != 1 {
		t.Fatal("native lost shared result", result, err)
	}
	if _, err = lease.Runtime().Saver(ctx, session.ProfileID(), session.catalog, "nyadb_edit", edits); !errors.Is(err, contract.ErrDataCommitUncertain) || writes.Load() != 1 {
		t.Fatal("lost shared replay", err)
	}
	verification, err := lease.Runtime().Verify(ctx, session.ProfileID(), session.catalog, "nyadb_edit", result)
	if err != nil || verification.Status != "applied" || verification.Recovered == nil || !verification.Recovered.Verified || writes.Load() != 1 {
		t.Fatal("native shared read-only verification", verification, err)
	}
}

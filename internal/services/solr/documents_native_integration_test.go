package solr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func documentNativeOpen(t *testing.T, ctx context.Context) *Client {
	t.Helper()
	cfg, secret := readNativeFixture(t)
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal("native document client", err)
	}
	t.Cleanup(c.Close)
	return c
}

func nativeDocumentRoundTrip(t *testing.T, ctx context.Context, ref NativeDocumentRef) {
	t.Helper()
	c := documentNativeOpen(t, ctx)
	created, err := c.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "create", Ref: ref, Version: "-1", Fields: map[string]json.RawMessage{"text_s": []byte(`"原生🐱"`), "exact_l": []byte(`9007199254740993`), "removed_s": []byte(`"remove"`)}})
	if err != nil || created == nil || created.State != "acknowledged" {
		t.Fatal("native create receipt", err)
	}
	first, err := c.ReadNativeDocument(ctx, ref)
	if err != nil || !strings.Contains(string(first.Projection), "9007199254740993") || created.Version != "" && created.Version != first.Version {
		t.Fatal("native exact create RTG", err)
	}
	updated, err := c.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "update", Ref: ref, Version: first.Version, Fields: map[string]json.RawMessage{"text_s": []byte(`"更新🐱"`), "removed_s": []byte(`null`)}})
	if err != nil || updated.State != "acknowledged" {
		t.Fatal("native atomic update receipt", err)
	}
	second, err := c.ReadNativeDocument(ctx, ref)
	if err != nil {
		t.Fatal("native atomic readback", err)
	}
	var fields map[string]json.RawMessage
	var text string
	if decodeJSON(second.Projection, &fields) != nil || decodeJSON(fields["text_s"], &text) != nil || text != "更新🐱" || second.Version == first.Version || updated.Version != "" && updated.Version != second.Version {
		t.Fatal("native atomic readback values/version", text, second.Version, updated.Version)
	}
	// An immediate native RTG/tlog projection can retain an explicit null;
	// an indexed projection can omit the same removed field. Preserve either.
	if value, ok := fields["removed_s"]; ok && string(value) != "null" {
		t.Fatal("native atomic unset readback", string(value))
	}
	// A complete native 409 still remains uncertain at the protocol layer;
	// it neither certifies remote rollback nor retries a retired client.
	conflict, err := c.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "update", Ref: ref, Version: first.Version, Fields: map[string]json.RawMessage{"text_s": []byte(`"stale"`)}})
	if !errors.Is(err, ErrDocumentMutationUncertain) || conflict == nil || conflict.Response.Status != 409 || len(conflict.Response.Body) == 0 {
		t.Fatal("native stale version receipt", err)
	}
	if _, err = c.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "delete", Ref: ref, Version: second.Version}); !errors.Is(err, ErrRetired) {
		t.Fatal("original conflicted client replayed", err)
	}
	fresh := documentNativeOpen(t, ctx)
	current, err := fresh.ReadNativeDocument(ctx, ref)
	if err != nil || current.Version != second.Version || strings.Contains(string(current.Projection), "stale") {
		t.Fatal("native conflict changed original document", err)
	}
	deleted, err := fresh.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "delete", Ref: ref, Version: current.Version})
	if err != nil || deleted.State != "acknowledged" {
		t.Fatal("native version delete", err)
	}
	t.Logf("native delete reported=%s mutation_clock=%s", deleted.ReportedVersion, deleted.Version)
	if _, err = fresh.ReadNativeDocument(ctx, ref); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatal("native deletion RTG", err)
	}
}

func TestSolrNativeDocumentCRUDCodecExactRTGAndConflict(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DOCUMENT_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native standalone document codec fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	nativeDocumentRoundTrip(t, ctx, NativeDocumentRef{Member: "nyadb_fixture", UniqueKey: "id", Key: contract.Cell{Kind: contract.Text, Text: "codec-native-🐱"}})
}

func TestSolrNativeDocumentLostReceiptKeepsOriginalMutationWithoutReplay(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DOCUMENT_IT") != "1" || os.Getenv("NYADB_SOLR_IT") != "1" {
		t.Skip("requires owned native standalone document codec fault fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin := documentNativeOpen(t, ctx)
	var writes atomic.Int32
	relay := nativeRelay(t, admin, func(w http.ResponseWriter, r *http.Request, response *Response) bool {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/update") {
			writes.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return true
			}
			_ = connection.Close()
			return true
		}
		return false
	})
	ref := NativeDocumentRef{Member: "nyadb_fixture", UniqueKey: "id", Key: contract.Cell{Kind: contract.Text, Text: "codec-lost-native"}}
	m := NativeDocumentMutation{Kind: "create", Ref: ref, Version: "-1", Fields: map[string]json.RawMessage{"text_s": []byte(`"applied-before-lost-reply"`)}}
	receipt, err := relay.MutateNativeDocument(ctx, m)
	if !errors.Is(err, ErrDocumentMutationUncertain) || receipt == nil || receipt.State != "uncertain" || writes.Load() != 1 {
		t.Fatal("native lost typed mutation", err)
	}
	if _, err = relay.MutateNativeDocument(ctx, m); !errors.Is(err, ErrRetired) || writes.Load() != 1 {
		t.Fatal("native typed request replayed", err)
	}
	actual, err := admin.ReadNativeDocument(ctx, ref)
	if err != nil || !strings.Contains(string(actual.Projection), "applied-before-lost-reply") {
		t.Fatal("original native mutation did not apply", err)
	}
}

func TestSolrNativeCloudDocumentCRUDCodecImplicitAndCompositeRouting(t *testing.T) {
	if os.Getenv("NYADB_SOLR_DOCUMENT_IT") != "1" || os.Getenv("NYADB_SOLR_CLOUD_IT") != "1" {
		t.Skip("requires owned native Cloud document codec fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	admin := documentNativeOpen(t, ctx)
	for _, mode := range []struct {
		router     string
		routeField bool
	}{{"implicit", false}, {"compositeId", false}, {"implicit", true}, {"compositeId", true}} {
		name := "nyadb_codec_" + mode.router
		if mode.routeField {
			name += "_field"
		}
		t.Run(name, func(t *testing.T) {
			router := mode.router
			query := url.Values{"action": {"CREATE"}, "name": {name}, "router.name": {router}, "replicationFactor": {"1"}, "collection.configName": {"owned_default"}, "wt": {"json"}}
			if router == "implicit" {
				query.Set("shards", "s1,s2")
			} else {
				query.Set("numShards", "2")
			}
			if mode.routeField {
				query.Set("router.field", "route_s")
			}
			nativeRun(t, admin, ctx, Request{Method: "GET", Path: "/admin/collections?" + query.Encode()})
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
				defer stop()
				if _, err := admin.Do(cleanup, Request{Method: "GET", Path: "/admin/collections?action=DELETE&name=" + name + "&wt=json"}); err != nil {
					t.Error("owned codec collection cleanup", err)
				}
			})
			ref := NativeDocumentRef{Member: name, UniqueKey: "id", Router: router, Key: contract.Cell{Kind: contract.Text, Text: "codec-cloud-native"}}
			if router == "implicit" {
				ref.Shard = "s2"
			}
			if mode.routeField {
				ref.RouteField = "route_s"
				ref.RouteValue = contract.Cell{Kind: contract.Text, Text: "native-tenant"}
				if router == "implicit" {
					ref.RouteValue.Text = ref.Shard
				}
			}
			var sibling *NativeDocument
			siblingRef := ref
			if router == "implicit" {
				siblingRef.Shard = "s1"
				if mode.routeField {
					siblingRef.RouteValue.Text = "s1"
				}
				if _, err := admin.MutateNativeDocument(ctx, NativeDocumentMutation{Kind: "create", Ref: siblingRef, Version: "-1", Fields: map[string]json.RawMessage{"text_s": []byte(`"sibling-original"`)}}); err != nil {
					t.Fatal("native routed sibling", err)
				}
				var err error
				sibling, err = admin.ReadNativeDocument(ctx, siblingRef)
				if err != nil {
					t.Fatal(err)
				}
			}
			nativeDocumentRoundTrip(t, ctx, ref)
			if sibling != nil {
				actual, err := admin.ReadNativeDocument(ctx, siblingRef)
				if err != nil || actual.Version != sibling.Version || string(actual.Projection) != string(sibling.Projection) {
					t.Fatal("typed mutation changed sibling with same key in another shard", err)
				}
			}
		})
	}
}

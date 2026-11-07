package easysearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/services/connections"
)

func nativeFixture(t *testing.T) (connections.EasysearchConfig, string) {
	t.Helper()
	if os.Getenv("NYADB_EASYSEARCH_IT") != "1" {
		t.Skip("requires an owned native Easysearch fixture")
	}
	raw, err := os.ReadFile(os.Getenv("NYADB_EASYSEARCH_CONFIG"))
	if err != nil {
		t.Fatal("private probe cannot be read")
	}
	var probe struct {
		Endpoint, Username, Auth string
		CAFile                   string `json:"ca_file"`
		ServerName               string `json:"server_name"`
		PasswordFile             string `json:"password_file"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		t.Fatal("invalid private probe")
	}
	secret, err := os.ReadFile(probe.PasswordFile)
	if err != nil {
		t.Fatal("private credential cannot be read")
	}
	return connections.EasysearchConfig{Endpoint: probe.Endpoint, Username: probe.Username, Auth: probe.Auth, CAFile: probe.CAFile, ServerName: probe.ServerName}, string(secret)
}

func ownedIndex(t *testing.T, c *Client, ctx context.Context) string {
	t.Helper()
	index := "nyadb_native_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := c.Do(ctx, Request{Method: "PUT", Path: "/" + index, Body: []byte(`{"settings":{"number_of_replicas":0,"number_of_shards":2},"mappings":{"properties":{"name":{"type":"keyword"},"exact":{"type":"long"}}}}`)}); err != nil {
		t.Fatal("owned index create", err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := c.Do(cleanup, Request{Method: "DELETE", Path: "/" + index}); err != nil {
			t.Error("owned index cleanup", err)
		}
	})
	return index
}

func TestEasysearchNativeTransportAuthenticationDocumentsAndPIT(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal("native identity", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error("native client cleanup", err)
		}
	})
	if id := c.Identity(); id.Version.Number != "2.5.1" || id.Version.Distribution != "easysearch" || id.ClusterUUID == "" {
		t.Fatal("native product identity")
	}
	if cfg.Auth != "none" {
		for name, test := range map[string]struct {
			config connections.EasysearchConfig
			secret string
		}{"wrong_password": {cfg, "wrong-password"}, "wrong_server_name": {func() connections.EasysearchConfig { v := cfg; v.ServerName = "wrong.invalid"; return v }(), secret}, "missing_CA": {func() connections.EasysearchConfig { v := cfg; v.CAFile = ""; return v }(), secret}} {
			t.Run(name, func(t *testing.T) {
				bad, err := Open(ctx, test.config, test.secret)
				if err == nil {
					_ = bad.Close()
					t.Fatal("invalid authentication or certificate accepted")
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatal("credential exposed in error")
				}
			})
		}
	}
	index := ownedIndex(t, c, ctx)
	firstID := DocumentID{Index: index, ID: "a/b🐱", Routing: "original-route"}
	originalSource := json.RawMessage(`{"name":"精确原文🐱","exact":9007199254740993,"decimal":1.123456789012345678901234567890,"null":null}`)
	created, err := c.CreateDocument(ctx, firstID, originalSource)
	if err != nil || created.Result != "created" || !bytes.Equal(created.Snapshot.Source, originalSource) {
		t.Fatal("native create", err)
	}
	for _, id := range []string{"b", "c"} {
		if _, err = c.CreateDocument(ctx, DocumentID{Index: index, ID: id}, json.RawMessage(`{"name":"other","exact":9007199254740995}`)); err != nil {
			t.Fatal("native fixture row", err)
		}
	}
	original, err := c.GetDocument(ctx, firstID)
	if err != nil || !bytes.Equal(original.Source, originalSource) || original.Identity != firstID {
		t.Fatal("exact original source and routing", err)
	}
	cursor, err := c.OpenCursor(ctx, index, json.RawMessage(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal("native PIT create", err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := cursor.Close(cleanup); err != nil {
			t.Error("original PIT close", err)
		}
	})
	page, err := cursor.Next(ctx)
	if err != nil || page.Total != 3 || len(page.Documents) != 1 || !page.HasMore {
		t.Fatal("native PIT first page", err)
	}
	boundary := cloneBoundary(page.After)
	reread, err := cursor.ReadAfter(ctx, nil, 1)
	if err != nil || reread.Documents[0].Identity != page.Documents[0].Identity {
		t.Fatal("original PIT explicit first-page boundary", err)
	}
	updatedSource := json.RawMessage(`{"name":"updated","exact":9007199254740997}`)
	updated, err := c.ReplaceDocument(ctx, original, updatedSource)
	if err != nil || updated.Snapshot.Sequence <= original.Sequence {
		t.Fatal("original native version replacement", err)
	}
	if _, err = c.ReplaceDocument(ctx, original, json.RawMessage(`{"name":"stale"}`)); err == nil {
		t.Fatal("stale original sequence accepted")
	} else {
		var conflict *HTTPError
		if !errors.As(err, &conflict) || conflict.Status != 409 {
			t.Fatal("native version conflict lost", err)
		}
	}
	latest, err := c.GetDocument(ctx, firstID)
	if err != nil || !bytes.Equal(latest.Source, updatedSource) {
		t.Fatal("conflict replay changed source", err)
	}
	if _, err = c.CreateDocument(ctx, DocumentID{Index: index, ID: "late"}, json.RawMessage(`{"name":"outside original PIT"}`)); err != nil {
		t.Fatal(err)
	}
	seen := map[DocumentID]bool{page.Documents[0].Identity: true}
	for page.HasMore {
		page, err = cursor.Next(ctx)
		if err != nil || page.Total != 3 || len(page.Documents) != 1 || seen[page.Documents[0].Identity] || page.Documents[0].Identity.ID == "late" {
			t.Fatal("original PIT multi-page scope", err)
		}
		seen[page.Documents[0].Identity] = true
	}
	if len(seen) != 3 {
		t.Fatal("native PIT lost original documents")
	}
	second, err := cursor.ReadAfter(ctx, boundary, 1)
	if err != nil || len(second.Documents) != 1 {
		t.Fatal("native explicit original boundary", err)
	}
	if err = cursor.Close(ctx); err != nil {
		t.Fatal("native exact PIT acknowledgement", err)
	}
	if _, err = cursor.Next(ctx); !errors.Is(err, ErrRetired) {
		t.Fatal("closed cursor resurrected", err)
	}
	if _, err = c.DeleteDocument(ctx, latest); err != nil {
		t.Fatal("native versioned delete", err)
	}
	if _, err = c.GetDocument(ctx, firstID); err == nil {
		t.Fatal("deleted original document still present")
	}
	response, err := c.Do(ctx, Request{Method: "GET", Path: "/" + index + "/_count"})
	var counted struct {
		Count int `json:"count"`
	}
	if err != nil || decodeJSON(response.Body, &counted) != nil || counted.Count != 3 {
		t.Fatal("native count", err)
	}
	// The native SQL endpoint remains a native REST request at this layer.
	response, err = c.Do(ctx, Request{Method: "POST", Path: "/_sql", Body: []byte(`{"query":"SELECT name, exact FROM ` + index + ` WHERE name = 'other' ORDER BY exact LIMIT 2"}`)})
	if err != nil || response.Status != 200 || !bytes.Contains(response.Body, []byte("9007199254740995")) {
		t.Fatal("native SQL plugin exact response", err)
	}
}

func TestEasysearchNativeAccessTokenUsesNativeHeaderAndPermissions(t *testing.T) {
	cfg, secret := nativeFixture(t)
	if cfg.Auth == "none" {
		t.Skip("native access tokens require the enabled security fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Error(err)
		}
	})
	index := ownedIndex(t, admin, ctx)
	body, _ := json.Marshal(map[string]any{"name": "nyadb-native-" + index, "expire_in": time.Now().Add(5 * time.Minute).Unix(), "cluster": []string{"cluster_monitor", "cluster_composite_ops"}, "indices": []any{map[string]any{"names": []string{index}, "privileges": []string{"crud", "indices_monitor"}}}})
	reply, err := admin.Do(ctx, Request{Method: "POST", Path: "/_security/access_token", Body: body})
	if err != nil {
		t.Fatal("native access token create", err)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if decodeJSON(reply.Body, &token) != nil || len(token.AccessToken) < 36 {
		t.Fatal("native access token receipt missing")
	}
	tokenID := token.AccessToken[:36]
	revoked := false
	t.Cleanup(func() {
		if revoked {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if _, err := admin.Do(cleanup, Request{Method: "DELETE", Path: "/_security/access_token/" + tokenID}); err != nil {
			t.Error("owned token cleanup", err)
		}
	})
	tokenCfg := cfg
	tokenCfg.Auth, tokenCfg.Username = "access_token", ""
	client, err := Open(ctx, tokenCfg, token.AccessToken)
	if err != nil {
		t.Fatal("native X-API-TOKEN root identity", err)
	}
	defer client.Close()
	id := DocumentID{Index: index, ID: "native-token"}
	if _, err = client.CreateDocument(ctx, id, []byte(`{"name":"native token","exact":9007199254740993}`)); err != nil {
		t.Fatal("native token write permission", err)
	}
	if _, err = client.GetDocument(ctx, id); err != nil {
		t.Fatal("native token read permission", err)
	}
	if wrong, err := Open(ctx, tokenCfg, "invalid-native-token"); err == nil {
		_ = wrong.Close()
		t.Fatal("invalid native token accepted")
	}
	if _, err = admin.Do(ctx, Request{Method: "DELETE", Path: "/_security/access_token/" + tokenID}); err != nil {
		t.Fatal("native token revoke", err)
	}
	revoked = true
	if _, err = client.Do(ctx, Request{Method: "GET", Path: "/"}); err == nil {
		t.Fatal("revoked native token still authenticated")
	}
}

func TestEasysearchNativePITKeepsEqualIDsOnDifferentRoutingShards(t *testing.T) {
	cfg, secret := nativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := Open(ctx, cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	index := ownedIndex(t, c, ctx)
	routes := map[int]string{}
	for i := 0; i < 32 && len(routes) < 2; i++ {
		route := "route-" + strconv.Itoa(i)
		r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + index + "/_search_shards?routing=" + url.QueryEscape(route)})
		if err != nil {
			t.Fatal(err)
		}
		var topology struct {
			Shards [][]struct {
				Shard int `json:"shard"`
			} `json:"shards"`
		}
		if decodeJSON(r.Body, &topology) != nil || len(topology.Shards) != 1 || len(topology.Shards[0]) != 1 {
			t.Fatal("native routing shard topology")
		}
		routes[topology.Shards[0][0].Shard] = route
	}
	if len(routes) != 2 {
		t.Fatal("fixture must use two different native routing shards")
	}
	for _, route := range routes {
		if _, err = c.CreateDocument(ctx, DocumentID{Index: index, ID: "same-id", Routing: route}, []byte(`{"name":"`+route+`"}`)); err != nil {
			t.Fatal("equal ID on another routing shard", err)
		}
	}
	a, err := c.OpenCursor(ctx, index, []byte(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.OpenCursor(ctx, index, []byte(`{"match_all":{}}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Next(ctx)
	if err != nil || len(first.Documents) != 1 || first.Total != 2 || !first.HasMore {
		t.Fatal("first routed hit", err)
	}
	second, err := a.Next(ctx)
	if err != nil || len(second.Documents) != 1 || second.Total != 2 || second.HasMore || second.Documents[0].Identity.ID != first.Documents[0].Identity.ID || second.Documents[0].Identity.Routing == first.Documents[0].Identity.Routing {
		t.Fatal("native offset skipped equal ID on another shard", err)
	}
	if _, err = c.CreateDocument(ctx, DocumentID{Index: index, ID: "late"}, []byte(`{"name":"after original snapshot"}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		reread, err := a.ReadAfter(ctx, nil, 1)
		if err != nil || len(reread.Documents) != 1 || reread.Total != 2 || reread.Documents[0].Identity != first.Documents[0].Identity {
			t.Fatal("original routed first page changed after refresh", err)
		}
	}
	if err = a.Close(ctx); err != nil {
		t.Fatal("one PIT close", err)
	}
	if sibling, err := b.Next(ctx); err != nil || sibling.Total != 2 {
		t.Fatal("one PIT close affected sibling snapshot", err)
	}
	if err = b.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

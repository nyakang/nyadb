package elasticsearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestElasticsearchNativeSessionIndexAliasDataStreamMetadataAndREST(t *testing.T) {
	cfg, secret := nativeConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin := nativeOpen(t, ctx)
	index := nativeIndex(t, admin, ctx)
	alias, stream, template := index+"_alias", index+"_stream", index+"_template"
	aliasBody, _ := json.Marshal(map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": alias, "filter": map[string]any{"term": map[string]int{"n": 1}}, "search_routing": "route-a"}}}})
	nativeRun(t, admin, ctx, "POST", "/_aliases", aliasBody)
	templateBody, _ := json.Marshal(map[string]any{"index_patterns": []string{stream}, "data_stream": map[string]any{}, "template": map[string]any{"settings": map[string]int{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": map[string]any{"properties": map[string]any{"@timestamp": map[string]string{"type": "date"}, "n": map[string]string{"type": "long"}}}}})
	nativeRun(t, admin, ctx, "PUT", "/_index_template/"+template, templateBody)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_index_template/" + template})
	}()
	nativeRun(t, admin, ctx, "PUT", "/_data_stream/"+stream, nil)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = admin.Do(cleanup, Request{Method: "DELETE", Path: "/_data_stream/" + stream})
	}()
	raw, err := OpenSession(ctx, sessionProfile(t, cfg), secret)
	if err != nil {
		t.Fatal(err)
	}
	s := raw.(*Session)
	defer s.Close()
	objects, err := s.Browse(ctx, s.Namespace(s.identity.ClusterName))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]contract.DBObject{}
	for _, o := range objects {
		byName[o.Name] = o
	}
	for name, kind := range map[string]string{index: "index", alias: "alias", stream: "data_stream"} {
		o, ok := byName[name]
		if !ok || o.Ref.Kind != kind || o.Ref.Catalog != admin.Identity().ClusterUUID || o.Ref.ProfileID != s.ProfileID() {
			t.Fatal("native target or qualified identity lost", name)
		}
		metadata, err := s.TableMetadata(ctx, o.Ref)
		if err != nil || metadata.Object != o.Ref || metadata.Editable || len(metadata.Columns) == 0 || len(metadata.Definitions) < 3 || !strings.Contains(metadata.Definition, "mappings") {
			t.Fatal("native mapping/target metadata incomplete", name, err)
		}
		if kind == "alias" && !strings.Contains(metadata.Definitions[2].SQL, "search_routing") {
			t.Fatal("alias filter/routing definition lost")
		}
	}
	foreign := s.Namespace("")
	foreign.Catalog = "other-cluster"
	if _, err = s.Browse(ctx, foreign); err == nil {
		t.Fatal("foreign cluster namespace accepted")
	}
	lease, err := s.ReserveQuery(22)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err = lease.Preconnect(ctx); err != nil {
		t.Fatal(err)
	}
	id := DocumentID{Index: index, ID: "lease"}
	query := "PUT /" + index + "/_create/lease\n{\"n\":9007199254740993}"
	result, err := lease.Execute(ctx, s.identity.ClusterName, query)
	if err != nil || len(result.Sets) != 1 || len(result.Sets[0].Rows) != 1 || result.Sets[0].Rows[0][0].Text != "201" || result.AffectedRows != 0 {
		t.Fatal("REST status converted to SQL row count", err)
	}
	result, err = lease.Execute(ctx, s.identity.ClusterName, "GET /"+index+"/_doc/lease")
	if err != nil || !strings.Contains(result.Sets[0].Rows[0][1].Text, "9007199254740993") || result.Truncated {
		t.Fatal("REST lease lost exact original response", err)
	}
	if _, err = lease.Execute(ctx, s.identity.ClusterName, query); err == nil {
		t.Fatal("native create conflict hidden")
	}
	if _, err = lease.Execute(ctx, s.identity.ClusterName, "GET /"+index+"/_doc/lease"); err != nil {
		t.Fatal("confirmed conflict retired healthy lease", err)
	}
	if _, err = admin.GetDocument(ctx, id); err != nil {
		t.Fatal("real lease write absent", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = lease.Execute(ctx, s.identity.ClusterName, "GET /"); !errors.Is(err, contract.ErrRevoked) {
		t.Fatal("closed original session still executed", err)
	}
}

package neo4j

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/graphschema"
)

func nativeSchemaLeaseTest(t *testing.T, s *Session, id uint64) contract.GraphSchemaPreparationLease {
	t.Helper()
	l, e := s.ReserveGraphSchemaPreparation(schemaObjectTest(s), id)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := l.Close(); e != nil {
			t.Error(e)
		}
	})
	return l
}
func nativeSchemaReadTest(t *testing.T, l contract.GraphSchemaPreparationLease) contract.GraphSchemaObservation {
	t.Helper()
	o, e := l.Read(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func schemaConfigTest(props ...contract.GraphProperty) contract.GraphValue {
	return contract.GraphValue{Kind: "map", Properties: []contract.GraphProperty{{Name: "indexConfig", Value: contract.GraphValue{Kind: "map", Properties: props}}}}
}
func executeSchemaPreviewTest(t *testing.T, c *Client, p contract.GraphSchemaPreview) Response {
	t.Helper()
	params, e := graphschema.Parameters(p.Request)
	if e != nil {
		t.Fatal(e)
	}
	// Native compiler acceptance only: production preparation has no Submit.
	return nativeRunTest(t, c, Request{Query: p.Request.Query, Database: p.Request.Database, Parameters: params})
}
func nativeSchemaDraftsTest(f nativeFixture) []contract.GraphSchemaDraft {
	label := "NyaSchema_" + f.Nonce + "`猫"
	var drafts []contract.GraphSchemaDraft
	for _, entity := range []string{"NODE", "RELATIONSHIP"} {
		for _, kind := range []string{"RANGE", "TEXT", "POINT", "FULLTEXT", "VECTOR"} {
			d := contract.GraphSchemaDraft{Action: "create_index", Name: markerTest(f, strings.ToLower(entity+"_"+kind)) + "` 猫", Type: kind, EntityType: entity, LabelsOrTypes: []string{label}, Properties: []string{strings.ToLower(kind)}}
			if kind == "FULLTEXT" {
				d.LabelsOrTypes = append(d.LabelsOrTypes, label+"second")
				d.Properties = append(d.Properties, "body")
				d.Options = schemaConfigTest(contract.GraphProperty{Name: "fulltext.analyzer", Value: Value{Kind: "string", Text: "standard-no-stop-words"}}, contract.GraphProperty{Name: "fulltext.eventually_consistent", Value: Value{Kind: "boolean", Text: "true"}})
			}
			if kind == "VECTOR" {
				similarity := "cosine"
				if entity == "RELATIONSHIP" {
					similarity = "euclidean"
				}
				d.Options = schemaConfigTest(contract.GraphProperty{Name: "vector.dimensions", Value: Value{Kind: "integer", Text: "3"}}, contract.GraphProperty{Name: "vector.similarity_function", Value: Value{Kind: "string", Text: similarity}})
			}
			drafts = append(drafts, d)
		}
		for _, action := range []string{"create_index", "create_constraint"} {
			d := contract.GraphSchemaDraft{Action: action, Name: markerTest(f, strings.ToLower(entity+"_"+action+"_composite")), Type: "RANGE", EntityType: entity, LabelsOrTypes: []string{label}, Properties: []string{"first", "second"}}
			if action == "create_constraint" {
				d.Type = "UNIQUE"
				d.Properties = []string{"unique_first", "unique_second"}
			}
			drafts = append(drafts, d)
		}
		drafts = append(drafts, contract.GraphSchemaDraft{Action: "create_constraint", Name: markerTest(f, strings.ToLower(entity+"_unique")), Type: "UNIQUE", EntityType: entity, LabelsOrTypes: []string{label}, Properties: []string{"unique"}})
	}
	return drafts
}

func TestNeo4jNativeSchemaCompleteCatalogAndTypedDDLPreviews(t *testing.T) {
	f := sessionFixtureTest(t)
	s := nativeSessionTest(t, f)
	admin := nativeOpenTest(t, f)
	l := nativeSchemaLeaseTest(t, s, 1)
	drafts := nativeSchemaDraftsTest(f)
	for _, draft := range drafts {
		t.Run(strings.ToLower(draft.EntityType+"_"+draft.Type+"_"+fmt.Sprint(len(draft.Properties))), func(t *testing.T) {
			before := nativeSchemaReadTest(t, l)
			prepared, e := l.Prepare(before, draft)
			if e != nil {
				t.Fatal(e)
			}
			preview := prepared.Preview()
			if preview.NonAtomicDrop || strings.Contains(preview.Request.Query, "IF NOT EXISTS") {
				t.Fatal("create silently skipped conflicts")
			}
			r := executeSchemaPreviewTest(t, admin, preview)
			counter := "indexes_added"
			if draft.Action == "create_constraint" {
				counter = "constraints_added"
			}
			if r.Summary.Counters[counter] != 1 {
				t.Fatal("missing original native DDL completion", r)
			}
			// Always clean this exact unique original schema, including failed assertions.
			dropped := false
			t.Cleanup(func() {
				if dropped {
					return
				}
				kind := "INDEX"
				if draft.Action == "create_constraint" {
					kind = "CONSTRAINT"
				}
				q := "DROP " + kind + " `" + strings.ReplaceAll(draft.Name, "`", "``") + "`"
				nativeRunTest(t, admin, Request{Query: q})
			})
			after := nativeSchemaReadTest(t, l)
			definitions, e := graphschema.Definitions(after.Snapshot())
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, definition := range definitions {
				kind := "index"
				if draft.Action == "create_constraint" {
					kind = "constraint"
				}
				if definition.Name == draft.Name && definition.Kind == kind {
					found = true
					if !reflect.DeepEqual(definition.LabelsOrTypes, draft.LabelsOrTypes) || !reflect.DeepEqual(definition.Properties, draft.Properties) || definition.CreateStatement == "" || definition.Options.Kind != "map" {
						t.Fatal("lost native ordered definition")
					}
				}
			}
			if !found {
				t.Fatal("native definition missing")
			}
			if _, e := l.Prepare(after, draft); !errors.Is(e, contract.ErrGraphSchemaExists) {
				t.Fatal("observed collision accepted", e)
			}
			dropAction := strings.Replace(draft.Action, "create_", "drop_", 1)
			drop, e := l.Prepare(after, contract.GraphSchemaDraft{Action: dropAction, Name: draft.Name})
			if e != nil {
				t.Fatal(e)
			}
			if !drop.Preview().NonAtomicDrop || len(drop.Preview().Before) != 1+map[bool]int{true: 1}[draft.Action == "create_constraint"] {
				t.Fatal("drop omitted backing effect or promised atomic compare")
			}
			r = executeSchemaPreviewTest(t, admin, drop.Preview())
			dropped = true
			counter = "indexes_removed"
			if draft.Action == "create_constraint" {
				counter = "constraints_removed"
			}
			if r.Summary.Counters[counter] != 1 {
				t.Fatal("missing native drop completion")
			}
		})
	}
}

func TestNeo4jNativeSchemaOriginalPreparationOwnershipAndRefresh(t *testing.T) {
	f := sessionFixtureTest(t)
	s := nativeSessionTest(t, f)
	l := nativeSchemaLeaseTest(t, s, 1)
	other := nativeSchemaLeaseTest(t, s, 2)
	before := nativeSchemaReadTest(t, l)
	draft := schemaDraftTest()
	draft.Name = markerTest(f, "pure_preview")
	plan, e := l.Prepare(before, draft)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Prepare(before, draft); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("foreign original lease accepted observation", e)
	}
	copy := before.Snapshot()
	copy.Indexes.Columns[0] = "changed"
	if before.Snapshot().Indexes.Columns[0] != "id" {
		t.Fatal("public native catalog aliases evidence")
	}
	next := nativeSchemaReadTest(t, l)
	if next.Snapshot().Sequence <= before.Snapshot().Sequence {
		t.Fatal("observation sequence did not advance")
	}
	if _, e = l.Prepare(before, draft); !errors.Is(e, contract.ErrGraphSchemaChanged) {
		t.Fatal("refreshed original lease accepted stale observation", e)
	}
	for _, unsupported := range []string{"NODE KEY", "EXISTS", "TYPE"} {
		d := draft.Clone()
		d.Action, d.Type = "create_constraint", unsupported
		if _, e = l.Prepare(next, d); !errors.Is(e, contract.ErrUnsupported) {
			t.Fatal("unverified constraint semantics supported", e)
		}
	}
	if e := l.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Prepare(next, draft); !errors.Is(e, contract.ErrRevoked) {
		t.Fatal("closed preparation followed new session", e)
	}
	if plan.Preview().Draft.Name != draft.Name || before.Snapshot().Object != schemaObjectTest(s) {
		t.Fatal("close discarded independent original evidence")
	}
	admin := nativeOpenTest(t, f)
	r := nativeRunTest(t, admin, Request{Query: "SHOW INDEXES YIELD name WHERE name = $name RETURN name", Parameters: map[string]any{"name": draft.Name}, Mode: "read"})
	if len(r.Rows) != 0 {
		t.Fatal("pure preparation submitted a native write")
	}
}

func TestNeo4jNativeSchemaPreparationCancellationAndOriginalClose(t *testing.T) {
	f := sessionFixtureTest(t)
	for _, action := range []string{"lost", "cancel", "lease-close", "session-close"} {
		t.Run(action, func(t *testing.T) {
			relay := newNativeRelay(t, f, "SHOW INDEXES YIELD * RETURN *")
			original := f
			u, _ := url.Parse(f.Config.URI)
			u.Scheme, u.Host = "bolt", fmt.Sprintf("localhost:%d", relay.listener.Addr().(*net.TCPAddr).Port)
			original.Config.URI, original.Config.CAFile, original.Config.ClientCertFile, original.Config.ClientKeyFile = u.String(), "", "", ""
			s := nativeSessionTest(t, original)
			l := nativeSchemaLeaseTest(t, s, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, e := l.Read(ctx); done <- e }()
			select {
			case <-relay.observed:
			case <-time.After(15 * time.Second):
				t.Fatal("original full SHOW not dispatched")
			}
			closed := make(chan error, 1)
			switch action {
			case "lost":
				relay.Close()
			case "cancel":
				cancel()
			case "lease-close":
				go func() { closed <- l.Close() }()
			case "session-close":
				go func() { closed <- s.Close() }()
			}
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("interrupted native schema read published")
				}
			case <-time.After(15 * time.Second):
				relay.Close()
				t.Fatal("original read did not acknowledge interruption")
			}
			if action == "lease-close" || action == "session-close" {
				select {
				case e := <-closed:
					if e != nil {
						t.Fatal(e)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("close did not acknowledge original worker")
				}
			}
			if _, e := l.Read(context.Background()); !errors.Is(e, contract.ErrRevoked) {
				t.Fatal("original read implicitly recovered/replayed", e)
			}
			if relay.dispatches.Load() != 1 {
				t.Fatal("original full SHOW retried")
			}
			replacement := nativeSessionTest(t, f)
			fresh := nativeSchemaLeaseTest(t, replacement, 1)
			_ = s.Close()
			_ = l.Close()
			if nativeSchemaReadTest(t, fresh).Snapshot().Object.ProfileID != s.profileID || !replacement.Alive() {
				t.Fatal("old cleanup reached replacement")
			}
		})
	}
}

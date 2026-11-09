package neo4j

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestNeo4jPropertyColumnsPreserveMissingEmptyExactBytesAndOriginalInventory(t *testing.T) {
	s := fakeSessionTest(t)
	label := "original"
	blob := bytes.Repeat([]byte{0, 255}, 800)
	nodes := []nd.Node{{ElementId: "a", Labels: []string{label}, Props: map[string]any{"blob": blob, "element_id": "property id", "empty": "", "exact": int64(9223372036854775807), "mixed": int64(1)}}, {ElementId: "b", Labels: []string{label}, Props: map[string]any{"mixed": "different kind"}}}
	schema := [][]any{{"blob", []any{"LIST<INTEGER NOT NULL> NOT NULL"}}, {"element_id", []any{"STRING NOT NULL"}}, {"empty", []any{"STRING NOT NULL"}}, {"exact", []any{"INTEGER NOT NULL"}}, {"mixed", []any{"STRING NOT NULL", "INTEGER NOT NULL"}}}
	opens := dataFakeOpenTest(t, s, func(_ context.Context, q string, p map[string]any) (nd.Result, error) {
		if p["name"] != label || strings.Contains(q, "property id") {
			t.Fatal("property projection lost original scope or interpolated a value")
		}
		result := dataResultTest([]string{"entity"})
		for _, node := range nodes {
			if target, ok := p["element_id"].(string); ok {
				if target != node.ElementId {
					continue
				}
			} else if node.ElementId <= p["after"].(string) {
				continue
			}
			result.rows = append(result.rows, &nd.Record{Values: []any{node}})
		}
		return result, nil
	}, func() nd.Result { return dataResultTest([]string{"property", "types"}, schema...) })
	l := dataLeaseTest(t, s, "label", label)
	ctx := context.Background()
	page, err := l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	column := func(name string) int {
		index := slices.Index(page.Columns, name)
		if index < 0 {
			t.Fatal("missing property column", name)
		}
		return index
	}
	if page.Rows[0][column("property.exact")].Value.Kind != contract.Integer || page.Rows[0][column("property.exact")].Text != "9223372036854775807" || page.Rows[0][column("property.empty")].Null || page.Rows[0][column("property.empty")].Text != "" || !page.Rows[1][column("property.empty")].Null || page.RowSnapshots[1].Fingerprints[column("property.empty")] != nil {
		t.Fatal("property projection conflated missing, empty or exact integer")
	}
	if page.Rows[0][column("property.element_id")].Text != "property id" || page.Rows[0][0].Text != "a" || page.DefaultColumnOrder[1] != "property.blob" || !strings.Contains(page.ColumnMetadata[column("property.mixed")].Type, "INTEGER") || page.Rows[1][column("property.mixed")].Value.Kind != contract.Text {
		t.Fatal("property collision, order or heterogeneous kinds lost")
	}
	preview := page.Rows[0][column("property.blob")]
	if !preview.Truncated || len(preview.Value.Bytes) != 1024 || len(preview.Text) != 2048 {
		t.Fatal("binary property preview bounds lost")
	}
	chunk, err := l.chunk(ctx, 42, "neo4j", label, "property.blob", *page.RowRefs[0], 1000, 1024)
	if err != nil || !chunk.Binary || chunk.TotalUnits != 1600 || chunk.NextOffset != 1600 || len(chunk.Text) != 1200 {
		t.Fatal("original binary property chunk changed units", err)
	}
	missing, err := l.chunk(ctx, 42, "neo4j", label, "property.empty", *page.RowRefs[1], 0, 1024)
	if err != nil || !missing.Null || missing.Text != "" {
		t.Fatal("missing property read became empty string", err)
	}
	old := cloneRowRef(*page.RowRefs[0])
	nodes[1].Props["new"] = "later"
	schema = append(schema, []any{"new", []any{"STRING NOT NULL"}})
	if _, err = l.page(ctx, 42, "neo4j", label, contract.DataOptions{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	row, err := l.row(ctx, 42, "neo4j", label, old)
	if err != nil || len(row.Columns) != len(page.Columns) || !bytes.Equal(row.Values["property.blob"].Bytes, blob) || row.Values["property.exact"].Kind != contract.Integer {
		t.Fatal("old field request adopted a later inventory or narrowed bytes/integer", err)
	}
	if _, exists := row.Values["property.new"]; exists {
		t.Fatal("new page changed original row column meanings")
	}
	var identity graphDataIdentity
	if json.Unmarshal(old.Values[0], &identity) != nil {
		t.Fatal("identity decode failed")
	}
	identity.Properties[0].Key = "foreign"
	old.Values[0], _ = json.Marshal(identity)
	before := opens.Load()
	if _, err = l.row(ctx, 42, "neo4j", label, old); err == nil || opens.Load() != before {
		t.Fatal("forged property mapping opened a client")
	}
	before = opens.Load()
	if err = l.exportRows(ctx, 42, "neo4j", label, contract.DataOptions{}, []string{"foreign"}, func([]contract.DataValue) error { return nil }); !errors.Is(err, contract.ErrUnsupported) || opens.Load() != before {
		t.Fatal("unsupported export field dispatched", err)
	}
}

func TestNeo4jPropertyInventoryRejectsOversizedOrMalformedColumns(t *testing.T) {
	for _, rows := range [][][]any{{{"duplicate", []any{"INTEGER NOT NULL"}}, {"duplicate", []any{"INTEGER NOT NULL"}}}, {{"name", []any{int64(1)}}}, {{strings.Repeat("x", 4097), []any{"STRING NOT NULL"}}}} {
		t.Run("invalid inventory", func(t *testing.T) {
			s := fakeSessionTest(t)
			reads := 0
			dataFakeOpenTest(t, s, func(context.Context, string, map[string]any) (nd.Result, error) {
				reads++
				return dataResultTest([]string{"entity"}), nil
			}, func() nd.Result { return dataResultTest([]string{"property", "types"}, rows...) })
			l := dataLeaseTest(t, s, "label", "original")
			if _, err := l.page(context.Background(), 42, "neo4j", "original", contract.DataOptions{Limit: 100}); err == nil || reads != 0 {
				t.Fatal("malformed property inventory reached entity read", err)
			}
		})
	}
}

func TestNeo4jPropertyFieldReadsDuringCloseRevokeOriginalInventory(t *testing.T) {
	s := fakeSessionTest(t)
	node := nd.Node{ElementId: "a", Labels: []string{"original"}, Props: map[string]any{"field": "value"}}
	opens := dataFakeOpenTest(t, s, func(context.Context, string, map[string]any) (nd.Result, error) {
		return dataResultTest([]string{"entity"}, []any{node}), nil
	}, func() nd.Result {
		return dataResultTest([]string{"property", "types"}, []any{"field", []any{"STRING NOT NULL"}})
	})
	l := dataLeaseTest(t, s, "label", "original")
	page, err := l.page(context.Background(), 42, "neo4j", "original", contract.DataOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ref := cloneRowRef(*page.RowRefs[0])
	start := make(chan struct{})
	entered := make(chan struct{}, 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, _ = l.chunk(context.Background(), 42, "neo4j", "original", "property.field", ref, 0, 1024)
			entered <- struct{}{}
			for range 100 {
				_, _ = l.chunk(context.Background(), 42, "neo4j", "original", "property.field", ref, 0, 1024)
			}
		}()
	}
	close(start)
	for range 32 {
		<-entered
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	before := opens.Load()
	if _, err = l.chunk(context.Background(), 42, "neo4j", "original", "property.field", ref, 0, 1024); err == nil || opens.Load() != before || len(l.propertyProjection()) != 0 {
		t.Fatal("closed property inventory dispatched a late field read or retained columns", err)
	}
}

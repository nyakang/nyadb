package neo4j

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// This native probe establishes the lock and transaction semantics needed by
// the shared grid. A returned entity before Commit is only a provisional value.
func TestNeo4jNativeDataWriteExplicitTransactionsLockComparisonRollbackAndCommit(t *testing.T) {
	f := fixtureTest(t)
	admin := nativeOpenTest(t, f)
	marker := markerTest(f, "data_write_transaction")
	t.Cleanup(func() { cleanupMarkerTest(t, admin, marker) })
	created := nativeRunTest(t, admin, Request{Query: "CREATE (a:NyaFoundation {marker:$marker,guard:'original'})-[r:NYA_GRID_WRITE {guard:'original'}]->(b:NyaFoundation {marker:$marker}) RETURN a,r", Parameters: map[string]any{"marker": marker}})
	for _, target := range []struct{ kind, pattern, id string }{
		{"node", "(entity)", created.Rows[0][0].Node.ElementID},
		{"relationship", "()-[entity]->()", created.Rows[0][1].Relationship.ElementID},
	} {
		t.Run(target.kind, func(t *testing.T) {
			query := "MATCH " + target.pattern + " WHERE elementId(entity)=$id SET entity[$key]=entity[$key] RETURN entity"
			original := nativeRunTest(t, admin, Request{Query: "MATCH " + target.pattern + " WHERE elementId(entity)=$id RETURN entity", Parameters: map[string]any{"id": target.id}}).Rows[0][0]
			holder := nativeOpenTest(t, f).driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
			defer holder.Close(context.Background())
			hold, err := holder.BeginTransaction(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Close(context.Background())
			r, err := hold.Run(context.Background(), "MATCH "+target.pattern+" WHERE elementId(entity)=$id SET entity.guard='concurrent' RETURN entity", map[string]any{"id": target.id})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Consume(context.Background()); err != nil {
				t.Fatal(err)
			}
			writer := nativeOpenTest(t, f).driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
			defer writer.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tx, err := writer.BeginTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Close(context.Background())
			type captured struct {
				value Value
				err   error
			}
			complete := make(chan captured, 1)
			go func() {
				r, err := tx.Run(ctx, query, map[string]any{"id": target.id, "key": "absent_property"})
				if err != nil {
					complete <- captured{err: err}
					return
				}
				record, err := r.Single(ctx)
				if err != nil {
					complete <- captured{err: err}
					return
				}
				budget := valueBudget{limit: 1 << 20}
				value, _, err := budget.capture(record.Values[0], true, 0)
				_, consumeErr := r.Consume(ctx)
				if err == nil {
					err = consumeErr
				}
				complete <- captured{value, err}
			}()
			blocked := false
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				show := nativeRunTest(t, admin, Request{Query: "SHOW TRANSACTIONS YIELD currentQuery,status WHERE currentQuery=$query RETURN status", Database: "system", Mode: "read", Parameters: map[string]any{"query": query}})
				if len(show.Rows) > 0 && strings.Contains(show.Rows[0][0].Text, "Blocked") {
					blocked = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !blocked {
				cancel()
				<-complete
				t.Fatal("dependent SET of an absent property did not obtain the entity lock")
			}
			if err = hold.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := <-complete
			if got.err != nil || fingerprint(got.value) == fingerprint(original) {
				t.Fatal("locked entity did not include the concurrent committed change", got.err)
			}
			if err = tx.Rollback(context.Background()); err != nil {
				t.Fatal(err)
			}
			read := nativeRunTest(t, admin, Request{Query: "MATCH " + target.pattern + " WHERE elementId(entity)=$id RETURN entity.guard,entity.absent_property", Mode: "read", Parameters: map[string]any{"id": target.id}})
			if read.Rows[0][0].Text != "concurrent" || read.Rows[0][1].Kind != "null" {
				t.Fatal("conflict rollback lost the concurrent value or introduced a lock property")
			}
			t.Log("native absent-property lock and post-lock full fingerprint comparison confirmed")
		})
	}

	writer := nativeOpenTest(t, f).driver.NewSession(context.Background(), nd.SessionConfig{DatabaseName: "neo4j"})
	defer writer.Close(context.Background())
	for _, commit := range []bool{false, true} {
		tx, err := writer.BeginTransaction(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []string{
			"MATCH (e) WHERE elementId(e)=$node SET e.amount=$amount,e.calendar=$calendar RETURN e",
			"MATCH ()-[e]->() WHERE elementId(e)=$relationship SET e.coords=$coords,e.items=$items RETURN e",
		} {
			r, err := tx.Run(context.Background(), query, map[string]any{"node": created.Rows[0][0].Node.ElementID, "relationship": created.Rows[0][1].Relationship.ElementID,
				"amount": int64(9223372036854775807), "calendar": nd.Date(time.Date(10000, 2, 29, 0, 0, 0, 0, time.UTC)), "coords": nd.Point3D{SpatialRefId: 4979, X: 113.5, Y: 22.3, Z: 10.5}, "items": []any{int64(-9223372036854775808), int64(9223372036854775807)}})
			if err != nil {
				t.Fatal(err)
			}
			record, err := r.Single(context.Background())
			if err != nil || len(record.Values) != 1 {
				t.Fatal("provisional native transaction receipt unavailable", err)
			}
			budget := valueBudget{limit: 1 << 20}
			if _, _, err = budget.capture(record.Values[0], true, 0); err != nil {
				t.Fatal(err)
			}
			summary, err := r.Consume(context.Background())
			if err != nil || summary.Database().Name() != "neo4j" || summary.Counters().PropertiesSet() != 2 {
				t.Fatal("native per-statement transaction summary unavailable", err)
			}
		}
		if commit {
			err = tx.Commit(context.Background())
		} else {
			err = tx.Rollback(context.Background())
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		read := nativeRunTest(t, admin, Request{Query: "MATCH (a)-[r:NYA_GRID_WRITE]->() WHERE elementId(a)=$id RETURN a.amount,a.calendar,r.coords,r.items", Mode: "read", Parameters: map[string]any{"id": created.Rows[0][0].Node.ElementID}})
		if !commit {
			for _, value := range read.Rows[0] {
				if value.Kind != "null" {
					t.Fatal("rollback retained an earlier node/relationship property change")
				}
			}
		} else if read.Rows[0][0].Text != "9223372036854775807" || read.Rows[0][1].Kind != "date" || read.Rows[0][2].Kind != "point3d" || !reflect.DeepEqual(read.Rows[0][3].Values, []Value{{Kind: "integer", Text: "-9223372036854775808"}, {Kind: "integer", Text: "9223372036854775807"}}) || len(writer.LastBookmarks()) == 0 {
			t.Fatal("confirmed commit lost precise typed values or final bookmark")
		}
		t.Log("native node/relationship atomic transaction", "committed", commit, "bookmark_count", len(writer.LastBookmarks()))
	}
}

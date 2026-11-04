package engines

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
	"github.com/nyakang/nyadb/internal/services/connections"
	"strings"
	"testing"
)

func TestSQLiteBrowseFlagsAndGroupedFiltersUseDirectoryAndSameMeaning(t *testing.T) {
	s := sqliteSession(t)
	run(t, s, `CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER, b INTEGER, c INTEGER); CREATE INDEX idx_t ON t(a); CREATE TRIGGER trg_t AFTER INSERT ON t BEGIN SELECT 1; END; INSERT INTO t VALUES (1,1,0,1),(2,0,1,1),(3,1,1,0),(4,0,0,1);`)
	list, err := s.Browse(context.Background(), s.Namespace("main"))
	if err != nil || len(list) != 1 || list[0].HasIndexes == nil || !*list[0].HasIndexes || !*list[0].HasTriggers || *list[0].RootPage <= 0 {
		t.Fatal("catalog flags", list, err)
	}
	leaf := func(col string) contract.FilterExpression[contract.Filter] {
		f := contract.Filter{Column: col, Operator: "=", Value: contract.Cell{Kind: contract.Integer, Text: "1"}}
		return contract.FilterExpression[contract.Filter]{Op: "rule", Rule: &f}
	}
	expression := &contract.FilterExpression[contract.Filter]{Op: "and", Children: []contract.FilterExpression[contract.Filter]{{Op: "or", Children: []contract.FilterExpression[contract.Filter]{leaf("a"), leaf("b")}}, leaf("c")}}
	page, err := s.Page(context.Background(), contract.PageRequest{Object: object(s, "t"), Limit: 10, Condition: expression})
	if err != nil || len(page.Rows) != 2 {
		t.Fatal("grouped meaning", len(page.Rows), err)
	}
	_, err = s.Page(context.Background(), contract.PageRequest{Object: object(s, "t"), Limit: 10, Condition: expression, Filters: []contract.Filter{{Column: "a", Operator: "=", Value: contract.Cell{Kind: contract.Integer, Text: "1"}}}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal("mixed expression accepted", err)
	}
}

func TestSQLiteGroupedTaskFixture(t *testing.T) { groupedTaskFixture(t, sqliteSession(t), "main") }
func TestPostgreSQL17GroupedTaskFixture(t *testing.T) {
	groupedTaskFixture(t, postgresDesignFixture(t), "public")
}
func groupedTaskFixture(t *testing.T, s *Session, schema string) {
	ctx := context.Background()
	o := s.Namespace(schema).Object("table", "ux_grouped")
	run(t, s, "CREATE TABLE "+qualified(o)+" (id INTEGER PRIMARY KEY,a INTEGER,b INTEGER,c INTEGER); INSERT INTO "+qualified(o)+" VALUES (1,1,0,1),(2,0,1,1),(3,1,1,0),(4,0,0,1)")
	leaf := func(col string) contract.FilterExpression[contract.DataFilterRule] {
		r := contract.DataFilterRule{Column: col, Mode: "equal", Value: "1"}
		return contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &r}
	}
	expr := &contract.FilterExpression[contract.DataFilterRule]{Op: "and", Children: []contract.FilterExpression[contract.DataFilterRule]{{Op: "or", Children: []contract.FilterExpression[contract.DataFilterRule]{leaf("a"), leaf("b")}}, leaf("c")}}
	r := contract.ComposeData(s)
	options := contract.DataOptions{Object: o, Condition: expr, Limit: 1, UseKeyset: true}
	first, err := r.AdvancedLoader(ctx, s.id, schema, o.Name, options)
	if err != nil || len(first.Rows) != 1 || first.Rows[0][0].Text != "1" || !first.HasMore {
		t.Fatalf("first page: %+v %v", first, err)
	}
	options.After = first.NextCursor
	options.Offset = 1
	next, err := r.AdvancedLoader(ctx, s.id, schema, o.Name, options)
	if err != nil || len(next.Rows) != 1 || next.Rows[0][0].Text != "2" || next.HasMore {
		t.Fatalf("next page: %+v %v", next, err)
	}
	options.Condition = contract.AndFilters([]contract.DataFilterRule{{Column: "c", Mode: "equal", Value: "0"}})
	if _, err = r.AdvancedLoader(ctx, s.id, schema, o.Name, options); err == nil {
		t.Fatal("changed expression accepted old cursor")
	}
	options = contract.DataOptions{Object: o, Condition: expr}
	analysis, err := r.Analyzer(ctx, s.id, schema, o.Name, options, []string{"id"})
	if err != nil || analysis.Rows != 2 {
		t.Fatal("analysis did not use grouped filters", analysis, err)
	}
	ids := []string{}
	err = r.Exporter(ctx, s.id, schema, o.Name, options, []string{"id"}, func(row []contract.DataValue) error { ids = append(ids, row[0].Text); return nil })
	if err != nil || fmt.Sprint(ids) != "[1 2]" {
		t.Fatal("export did not use grouped filters", ids, err)
	}
	view := s.Namespace(schema).Object("view", "ux_grouped_view")
	run(t, s, "CREATE VIEW "+qualified(view)+" AS SELECT id,a FROM "+qualified(o))
	metadata, err := s.TableMetadata(ctx, view)
	if err != nil || len(metadata.Columns) != 2 {
		t.Fatal("view completion metadata", metadata, err)
	}
	sql, err := queryplan.Build(string(s.kind), "SELECT * FROM "+qualified(o)+" WHERE a=1")
	if err != nil || strings.Contains(sql, "ANALYZE") {
		t.Fatal(sql, err)
	}
	lease, err := s.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	result, err := lease.Execute(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := queryplan.Parse(string(s.kind), result)
	if err != nil || len(plan.Nodes) == 0 {
		t.Fatal("real structured explain", plan, err)
	}
	if s.kind == connections.PostgreSQL {
		run(t, s, "ANALYZE "+qualified(o))
		objects, err := s.Browse(ctx, s.Namespace(schema))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, object := range objects {
			if object.Name == o.Name {
				found = true
				if object.RowsEstimate == nil || *object.RowsEstimate != 4 || object.DataLength == nil || *object.DataLength <= 0 {
					t.Fatal("relation estimates or size missing", object)
				}
			}
		}
		if !found {
			t.Fatal("relation missing from metadata")
		}
		statement := "SELECT '中文🙂', missing_column FROM " + qualified(o)
		_, err = lease.Execute(ctx, statement)
		var pg *pgconn.PgError
		d := contract.QueryDiagnosis(err)
		if d == nil || d.Position == nil || !errors.As(err, &pg) || *d.Position != strings.Index(statement, "missing_column") {
			t.Fatal("real UTF-8 driver diagnostic", d, err)
		}
	}
}

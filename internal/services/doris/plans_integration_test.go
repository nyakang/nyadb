package doris

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
)

func TestDorisNativeNereidsPlansFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	ctx := context.Background()
	s, err := OpenSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Capabilities().Explain || s.Capabilities().EditableTables || s.Capabilities().Transactions {
		t.Fatal("native plan received OLTP capabilities")
	}
	provider, err := contract.Require[contract.QueryProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveQuery(5)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, err := l.Execute(ctx, p.Database, q)
		if err != nil {
			t.Fatalf("native plan fixture command failed: %v", err)
		}
		return r
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	a, b := quote("plan_a_"+suffix), quote("plan_b_"+suffix)
	v := quote("plan_v_" + suffix)
	for _, name := range []string{a, b} {
		run("CREATE TABLE " + name + "(id BIGINT,n BIGINT,note STRING) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1')")
		defer func(name string) { _, _ = l.Execute(ctx, p.Database, "DROP TABLE "+name) }(name)
		run("INSERT INTO " + name + " VALUES(1,3,'one'),(2,4,'two')")
	}
	run("CREATE VIEW " + v + " AS SELECT * FROM " + a)
	defer func() { _, _ = l.Execute(ctx, p.Database, "DROP VIEW "+v) }()
	run("SET @nyadb_plan_session=987654321")
	queries := []string{
		"SELECT 1",
		"SELECT note FROM " + a + " WHERE id=1",
		"SELECT note FROM " + a + " WHERE id>0 ORDER BY id LIMIT 2",
		"SELECT note FROM " + v + " WHERE id=1",
		"SELECT a.note,SUM(b.n) FROM " + a + " a JOIN " + b + " b ON a.id=b.id WHERE b.n>0 GROUP BY a.note ORDER BY SUM(b.n) DESC LIMIT 2",
		"SELECT id FROM " + a + " UNION ALL SELECT id FROM " + b,
		"WITH t AS (SELECT id FROM " + a + ") SELECT id FROM t WHERE id>0",
		"SELECT id,ROW_NUMBER() OVER(ORDER BY n) FROM " + a,
		"INSERT INTO " + a + " VALUES(99,99,'must not execute')",
	}
	seen := map[string]bool{}
	for _, source := range queries {
		text, err := queryplan.Build("doris", source)
		if err != nil {
			t.Fatal(err)
		}
		r := run(text)
		plan, err := queryplan.Parse("doris", r)
		if err != nil {
			t.Fatalf("native Nereids shape rejected for %s: %v\n%+v", source, err, r.Sets)
		}
		if len(r.Sets) != 1 || r.Truncated || len(plan.Nodes) != 1 || plan.Nodes[0].Cost == nil || plan.Nodes[0].Rows != nil {
			t.Fatal("native whole-plan cost or original result lost")
		}
		var walk func(contract.PlanNode)
		walk = func(node contract.PlanNode) {
			label := strings.Split(node.Label, "[")[0]
			seen[label] = true
			if node.Cost != nil || label == "PhysicalResultSink" && node.Rows != nil {
				t.Fatal("optimizer total was copied onto an operator")
			}
			if label == "PhysicalOlapScan" && node.Rows == nil {
				t.Fatal("native scan row estimate missing")
			}
			for _, child := range node.Children {
				walk(child)
			}
		}
		walk(plan.Nodes[0].Children[0])
	}
	for _, operator := range []string{"PhysicalResultSink", "PhysicalOneRowRelation", "PhysicalOlapScan", "PhysicalLazyMaterialize", "PhysicalLazyMaterializeOlapScan", "PhysicalFilter", "PhysicalHashJoin", "PhysicalHashAggregate", "PhysicalTopN", "PhysicalDistribute", "PhysicalUnion", "PhysicalWindow", "PhysicalOlapTableSink"} {
		if !seen[operator] {
			t.Fatalf("native operator not observed: %s", operator)
		}
	}
	r := run("SELECT COUNT(*),@nyadb_plan_session FROM " + a)
	if r.Sets[0].Rows[0][0].Text != "2" || r.Sets[0].Rows[0][1].Text != "987654321" {
		t.Fatal("EXPLAIN executed INSERT or changed original physical session")
	}
	plain := run("EXPLAIN SELECT 1")
	if len(plain.Sets[0].Rows) == 0 {
		t.Fatal("manual textual explain lost")
	}
	if _, err := queryplan.Parse("doris", plain); err == nil {
		t.Fatal("fragment plan misinterpreted as Nereids optimized tree")
	}
}

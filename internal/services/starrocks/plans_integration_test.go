package starrocks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/queryplan"
)

func TestStarRocksNativeEstimatedPlansFixture(t *testing.T) {
	p, password := fixtureProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	s, err := OpenSession(ctx, p, password)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Capabilities().Explain || s.Capabilities().EditableTables || s.Capabilities().Transactions {
		t.Fatal("plan capability inherited OLTP semantics")
	}
	provider, err := contract.Require[contract.QueryProvider](s)
	if err != nil {
		t.Fatal(err)
	}
	l, err := provider.ReserveQuery(20)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, err := l.Execute(ctx, p.Database, q)
		if err != nil {
			t.Fatalf("native plan fixture command failed for %s: %v", q, err)
		}
		return r
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	a, b, primary, view := quote("plan_a_"+suffix), quote("plan_b_"+suffix), quote("plan_pk_"+suffix), quote("plan_v_"+suffix)
	for _, name := range []string{a, b} {
		run("CREATE TABLE " + name + "(id BIGINT NOT NULL,n BIGINT,note VARCHAR(200)) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES('replication_num'='1')")
		defer func(name string) { _, _ = l.Execute(context.Background(), p.Database, "DROP TABLE IF EXISTS "+name) }(name)
		run("INSERT INTO " + name + " VALUES(1,3,'one'),(2,4,'two')")
	}
	run("CREATE TABLE " + primary + "(id BIGINT NOT NULL,note VARCHAR(200)) PRIMARY KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES('replication_num'='1')")
	defer func() { _, _ = l.Execute(context.Background(), p.Database, "DROP TABLE IF EXISTS "+primary) }()
	run("INSERT INTO " + primary + " VALUES(1,'one'),(2,'two')")
	run("CREATE VIEW " + view + " AS SELECT * FROM " + a)
	defer func() { _, _ = l.Execute(context.Background(), p.Database, "DROP VIEW IF EXISTS "+view) }()
	run("SET @nyadb_plan_session=987654321")
	queries := []string{
		"SELECT 1",
		"SELECT id FROM " + a + " WHERE FALSE",
		"SELECT note FROM " + a + " WHERE id=1",
		"SELECT note FROM " + a + " WHERE id>0 ORDER BY id LIMIT 2",
		"SELECT note FROM " + view + " WHERE id=1",
		"SELECT a.note,SUM(b.n) FROM " + a + " a JOIN " + b + " b ON a.id=b.id WHERE b.n>0 GROUP BY a.note ORDER BY SUM(b.n) DESC LIMIT 2",
		"SELECT id FROM " + a + " UNION ALL SELECT id FROM " + b,
		"WITH t AS (SELECT id FROM " + a + ") SELECT a.id FROM t a JOIN t b ON a.id=b.id",
		"SELECT id,ROW_NUMBER() OVER(ORDER BY n) FROM " + a,
		"INSERT INTO " + a + " VALUES(99,99,'must not execute')",
		"UPDATE " + primary + " SET note='must not execute' WHERE id=1",
		"DELETE FROM " + primary + " WHERE id=1",
	}
	seen := map[string]bool{}
	type capture struct {
		SQL    string
		Result contract.SQLResult
		Plan   *contract.ExecutionPlan
	}
	var captures []capture
	check := func(source string) *contract.ExecutionPlan {
		t.Helper()
		q, err := queryplan.Build("starrocks", source)
		if err != nil || !strings.HasPrefix(q, "EXPLAIN COSTS ") {
			t.Fatal("invalid native estimated request", err)
		}
		r := run(q)
		plan, err := queryplan.Parse("starrocks", r)
		if err != nil {
			t.Fatalf("native shape rejected for %s: %v\n%+v", source, err, r.Sets)
		}
		if len(plan.Nodes) != 1 || plan.Nodes[0].Rows != nil || plan.Nodes[0].Cost != nil || len(plan.Nodes[0].Children) < 2 {
			t.Fatal("whole-plan metrics were copied or fragments lost")
		}
		metrics := plan.Nodes[0].Children[0]
		if metrics.Label != "PLAN COST" || len(metrics.Children) != 2 || metrics.Children[0].Cost == nil || metrics.Children[1].Cost == nil {
			t.Fatal("native CPU/memory optimizer estimates lost")
		}
		var walk func(contract.PlanNode)
		walk = func(n contract.PlanNode) {
			seen[n.Label] = true
			if n.Cost != nil {
				t.Fatal("whole-plan cost copied onto native operator")
			}
			for _, child := range n.Children {
				walk(child)
			}
		}
		for _, fragment := range plan.Nodes[0].Children[1:] {
			if !strings.Contains(fragment.Detail, "Input Partition:") || fragment.Cost != nil || fragment.Rows != nil {
				t.Fatal("fragment properties lost or estimates inferred")
			}
			walk(fragment)
		}
		captures = append(captures, capture{q, r, plan})
		return plan
	}
	for _, source := range queries {
		check(source)
	}
	run("SET cbo_cte_reuse=true,cbo_cte_reuse_rate=0")
	multicast := check("WITH t AS(SELECT id,SUM(n) AS n FROM " + a + " GROUP BY id) SELECT a.id,a.n+b.n FROM t a JOIN t b ON a.id=b.id")
	foundMulticast := false
	for _, fragment := range multicast.Nodes[0].Children[1:] {
		if strings.Contains(fragment.Detail, "MultiCastDataSinks:") {
			foundMulticast = strings.Count(fragment.Detail, "OutPut Exchange Id:") == 2
		}
	}
	if !foundMulticast {
		t.Fatal("native CTE multicast sink or both destinations missing")
	}
	// Same-fragment right-deep joins exercise nested side prefixes followed
	// by the main child at an earlier prefix, rather than just unary chains.
	ca, cb, cc := quote("side_a_"+suffix), quote("side_b_"+suffix), quote("side_c_"+suffix)
	for _, name := range []string{ca, cb, cc} {
		run("CREATE TABLE " + name + "(id BIGINT NOT NULL) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES('replication_num'='1','colocate_with'='plan_" + suffix + "')")
		defer func(name string) { _, _ = l.Execute(context.Background(), p.Database, "DROP TABLE IF EXISTS "+name) }(name)
		run("INSERT INTO " + name + " VALUES(1),(2)")
	}
	run("SET disable_join_reorder=true")
	nested := check("SELECT a.id,x.id FROM " + ca + " a LEFT JOIN (SELECT b.id FROM " + cb + " b JOIN " + cc + " c ON b.id=c.id) x ON a.id=x.id")
	foundNested := false
	var nestedWalk func(contract.PlanNode)
	nestedWalk = func(n contract.PlanNode) {
		if strings.HasSuffix(n.Label, ":HASH JOIN") && len(n.Children) == 2 && strings.Contains(n.Detail, "LEFT OUTER JOIN (COLOCATE)") {
			right := n.Children[1]
			foundNested = len(right.Children) == 1 && strings.HasSuffix(right.Children[0].Label, ":HASH JOIN") && len(right.Children[0].Children) == 2
		}
		for _, child := range n.Children {
			nestedWalk(child)
		}
	}
	nestedWalk(nested.Nodes[0])
	if !foundNested {
		t.Fatal("native nested side branch did not retain logical child order")
	}
	for _, operator := range []string{"Project", "OlapScanNode", "UNION", "TOP-N", "MERGING-EXCHANGE", "HASH JOIN", "AGGREGATE (merge finalize)", "ANALYTIC"} {
		found := false
		for label := range seen {
			found = found || strings.HasSuffix(label, ":"+operator)
		}
		if !found {
			t.Fatalf("native operator not observed: %s", operator)
		}
	}
	result := run("SELECT COUNT(*) FROM " + a + "; SELECT @nyadb_plan_session")
	if result.Sets[0].Rows[0][0].Text != "2" || result.Sets[1].Rows[0][0].Text != "987654321" {
		t.Fatal("EXPLAIN executed INSERT or replaced original physical lease")
	}
	result = run("SELECT id,note FROM " + primary + " ORDER BY id")
	if len(result.Sets[0].Rows) != 2 || result.Sets[0].Rows[0][1].Text != "one" || result.Sets[0].Rows[1][1].Text != "two" {
		t.Fatal("EXPLAIN executed UPDATE or DELETE")
	}
	plain := run("EXPLAIN SELECT 1")
	if _, err := queryplan.Parse("starrocks", plain); err == nil || len(plain.Sets[0].Rows) == 0 {
		t.Fatal("manual EXPLAIN was lost or misinterpreted as COSTS")
	}
	if directory := os.Getenv("NYADB_STARROCKS_PLAN_DIR"); directory != "" {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(captures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "native-plans.json"), append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

package trino

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

func TestTrinoNativeDistributedEstimatedPlansFixture(t *testing.T) {
	p, secret := nativeProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	s, err := OpenSession(ctx, p, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := s.(*Session)
	if _, err = raw.Namespaces(ctx); err != nil {
		t.Fatal(err)
	}
	label := namespaceLabel("memory", "default")
	l, err := raw.ReserveQuery(20)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	run := func(q string) contract.SQLResult {
		t.Helper()
		r, e := l.Execute(ctx, label, q)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	table := "nyadb_plan_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	run("CREATE TABLE memory.default." + table + "(id BIGINT,note VARCHAR)")
	defer func() { _, _ = l.Execute(context.Background(), label, "DROP TABLE IF EXISTS memory.default."+table) }()
	run("INSERT INTO memory.default." + table + " VALUES(1,'original')")
	run("SET SESSION query_max_run_time='2h'")
	cases := map[string]string{
		"constant":      "SELECT 1",
		"empty":         "SELECT nationkey FROM tpch.tiny.nation WHERE FALSE",
		"scan":          "SELECT nationkey,name FROM tpch.tiny.nation WHERE nationkey>1 ORDER BY nationkey LIMIT 2",
		"join":          "SELECT r.name,COUNT(*) FROM tpch.tiny.nation n JOIN tpch.tiny.region r ON n.regionkey=r.regionkey GROUP BY r.name ORDER BY COUNT(*) DESC",
		"union":         "SELECT nationkey FROM tpch.tiny.nation UNION ALL SELECT regionkey FROM tpch.tiny.region",
		"window":        "SELECT name,ROW_NUMBER() OVER(PARTITION BY regionkey ORDER BY nationkey) FROM tpch.tiny.nation",
		"cte":           "WITH n AS(SELECT nationkey FROM tpch.tiny.nation) SELECT a.nationkey FROM n a JOIN n b ON a.nationkey=b.nationkey",
		"fused_project": "SELECT nationkey+1 FROM tpch.tiny.nation WHERE nationkey>1",
		"insert":        "INSERT INTO memory.default." + table + " VALUES(99,'must not execute')",
	}
	seen := map[string]bool{}
	fullCell := false
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			q, e := queryplan.Build("trino", source)
			if e != nil {
				t.Fatal(e)
			}
			r, e := l.Execute(ctx, label, q)
			if e != nil {
				t.Fatal(e)
			}
			plan, e := queryplan.Parse("trino", r)
			if e != nil {
				t.Fatalf("native plan rejected: %s: %v", name, e)
			}
			if plan.Engine != "trino" || len(plan.Nodes) != 1 || plan.Nodes[0].Rows != nil || plan.Nodes[0].Cost != nil {
				t.Fatal("invalid native plan envelope")
			}
			var walk func(contract.PlanNode)
			walk = func(n contract.PlanNode) {
				seen[n.Label] = true
				if strings.Contains(n.Label, ":") && n.Cost != nil {
					t.Fatal("unlike optimizer costs combined")
				}
				for _, c := range n.Children {
					walk(c)
				}
			}
			walk(plan.Nodes[0])
			cell := r.Sets[0].Rows[0][0]
			if len(cell.Text) > previewCellBytes {
				fullCell = true
				if cell.Truncated {
					t.Fatal("native JSON plan was clipped to ordinary preview")
				}
			}
			if folder := os.Getenv("NYADB_TRINO_PLAN_DIR"); folder != "" {
				if e = os.MkdirAll(folder, 0755); e != nil {
					t.Fatal(e)
				}
				encoded, _ := json.MarshalIndent(struct {
					SQL    string
					Result contract.SQLResult
					Plan   *contract.ExecutionPlan
				}{q, r, plan}, "", "  ")
				if e = os.WriteFile(filepath.Join(folder, name+".json"), encoded, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
	if !fullCell || !seen["CPU"] || !seen["Memory"] || !seen["Network"] {
		t.Fatal("complete plans or independent optimizer metrics lost")
	}
	r := run("SELECT id,note FROM memory.default." + table)
	if len(r.Sets[0].Rows) != 1 || r.Sets[0].Rows[0][0].Text != "1" || r.Sets[0].Rows[0][1].Text != "original" {
		t.Fatal("EXPLAIN executed INSERT")
	}
	r = run("SELECT current_catalog,current_schema")
	if r.Sets[0].Rows[0][0].Text != "memory" || r.Sets[0].Rows[0][1].Text != "default" {
		t.Fatal("EXPLAIN replaced original logical session")
	}
	r = run("SHOW SESSION LIKE 'query_max_run_time'")
	if len(r.Sets[0].Rows) != 1 || r.Sets[0].Rows[0][1].Text != "2h" {
		t.Fatal("EXPLAIN changed original session property")
	}
	r = run("SELECT array_join(repeat('长',3000),'')")
	if !r.Sets[0].Rows[0][0].Truncated || len(r.Sets[0].Rows[0][0].Text) > previewCellBytes {
		t.Fatal("ordinary SQL escaped preview bounds")
	}
}

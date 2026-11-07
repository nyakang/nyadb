package queryplan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func trinoPlanResult(raw string) contract.SQLResult {
	return contract.SQLResult{Sets: []contract.ResultSet{{Columns: []contract.Column{{Name: "Query Plan"}}, Rows: [][]contract.Cell{{{Kind: contract.Text, Text: raw}}}}}}
}

func trinoPlanFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "trino", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTrinoNativeFragmentGraphsAndIndependentEstimates(t *testing.T) {
	for _, name := range []string{"constant", "empty", "scan", "join", "union", "window", "cte", "fused_project", "insert"} {
		t.Run(name, func(t *testing.T) {
			plan, err := Parse("trino", trinoPlanResult(trinoPlanFixture(t, name)))
			if err != nil || plan.Engine != "trino" || len(plan.Nodes) != 1 {
				t.Fatal("native fixture rejected", err)
			}
			root := plan.Nodes[0]
			if root.Rows != nil || root.Cost != nil {
				t.Fatal("estimates fabricated for graph root")
			}
			var walk func(contract.PlanNode)
			walk = func(n contract.PlanNode) {
				if strings.HasPrefix(n.Label, "Estimate ") {
					if len(n.Children) != 3 || n.Children[0].Label != "CPU" || n.Children[1].Label != "Memory" || n.Children[2].Label != "Network" || n.Cost != nil {
						t.Fatal("unlike costs combined")
					}
				}
				if strings.Contains(n.Label, ":") && n.Cost != nil {
					t.Fatal("operator cost fabricated")
				}
				for _, c := range n.Children {
					walk(c)
				}
			}
			for _, fragment := range root.Children {
				if !strings.HasPrefix(fragment.Label, "Fragment ") || fragment.Rows != nil || fragment.Cost != nil || len(fragment.Children) != 1 {
					t.Fatal("fragment identity lost")
				}
				walk(fragment)
			}
		})
	}
	p, err := Parse("trino", trinoPlanResult(trinoPlanFixture(t, "scan")))
	if err != nil {
		t.Fatal(err)
	}
	fused := false
	var walk func(contract.PlanNode)
	walk = func(n contract.PlanNode) {
		if strings.HasSuffix(n.Label, ":ScanFilter") {
			fused = true
			if n.Rows == nil || len(n.Children) < 2 || n.Children[0].Rows == nil || n.Children[1].Rows == nil || *n.Children[0].Rows != 25 || *n.Children[1].Rows == 25 || *n.Rows != *n.Children[1].Rows {
				t.Fatal("fused scan/filter estimates borrowed each other's rows")
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(p.Nodes[0])
	if !fused {
		t.Fatal("fused native estimates lost")
	}
}

func TestTrinoAmbiguousIncompleteAndTruncatedPlansRemainRaw(t *testing.T) {
	raw := trinoPlanFixture(t, "constant")
	for _, bad := range []string{
		strings.Replace(raw, `"0" : {`, `"0" : {}, "0" : {`, 1),
		strings.Replace(raw, `"name" : "Output"`, `"name" : "Output", "name" : "Other"`, 1),
		strings.Replace(raw, `"outputRowCount" : 1.0`, `"outputRowCount" : 1.0, "outputRowCount" : 9.0`, 1),
		strings.Replace(raw, `"outputRowCount" : 1.0`, `"outputRowCount" : -1`, 1),
		strings.Replace(raw, `"cpuCost" : 0.0`, `"cpuCost" : null`, 1),
		strings.Replace(raw, `"cpuCost" : 0.0`, `"cpuCost" : "123"`, 1),
		strings.Replace(raw, `"cpuCost" : 0.0`, `"cpuCost" : 1e400`, 1),
		strings.Replace(raw, `"id" : "0"`, `"id" : "5"`, 1),
		strings.Replace(raw, `"children" : [ ]`, `"children" : null`, 1),
		strings.Replace(raw, `"0" : {`, `"1" : {`, 1),
		raw + raw,
		strings.Repeat(" ", 1<<20) + raw,
		strings.Replace(raw, `"name" : "Output"`, "\"name\" : \"\xff\"", 1),
	} {
		if _, err := Parse("trino", trinoPlanResult(bad)); err == nil {
			t.Fatal("ambiguous/invalid native plan accepted")
		}
	}
	for _, mutate := range []func(*contract.SQLResult){
		func(r *contract.SQLResult) { r.Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Truncation = []string{"cell"} },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Truncated = true },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Kind = contract.JSON },
		func(r *contract.SQLResult) { r.Sets[0].Rows[0][0].Bytes = []byte("opaque") },
		func(r *contract.SQLResult) { r.Sets[0].Columns[0].Name = "Explain String" },
		func(r *contract.SQLResult) { r.Sets[0].Rows = append(r.Sets[0].Rows, r.Sets[0].Rows[0]) },
	} {
		r := trinoPlanResult(raw)
		mutate(&r)
		if _, err := Parse("trino", r); err == nil {
			t.Fatal("truncated or foreign result accepted")
		}
	}
	for _, bad := range []string{
		strings.Replace(trinoPlanFixture(t, "scan"), `"sourceFragmentIds" : "[1]"`, `"sourceFragmentIds" : "[999]"`, 1),
		strings.Replace(trinoPlanFixture(t, "scan"), `"sourceFragmentIds" : "[1]"`, `"sourceFragmentIds" : "[0]"`, 1),
		strings.Replace(trinoPlanFixture(t, "scan"), `"sourceFragmentIds" : "[1]"`, `"sourceFragmentIds" : "[1, 1]"`, 1),
	} {
		if _, err := Parse("trino", trinoPlanResult(bad)); err == nil {
			t.Fatal("invalid fragment edge accepted")
		}
	}
	var fragments map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trinoPlanFixture(t, "scan")), &fragments); err != nil {
		t.Fatal(err)
	}
	delete(fragments, "1")
	broken, _ := json.Marshal(fragments)
	if _, err := Parse("trino", trinoPlanResult(string(broken))); err == nil {
		t.Fatal("lost producer fragment rendered as complete")
	}
	unknown := strings.Replace(raw, `"outputRowCount" : 1.0`, `"outputRowCount" : "NaN"`, 1)
	p, err := Parse("trino", trinoPlanResult(unknown))
	if err != nil || p.Nodes[0].Children[0].Children[0].Rows != nil {
		t.Fatal("unknown estimate became a number", err)
	}
}

func TestTrinoBuildOneNonExecutingNativePlan(t *testing.T) {
	for _, q := range []string{"SELECT 1", "--native comment\nSELECT 'ordinary\\'", "INSERT INTO t VALUES(1)", "UPDATE t SET n=1", "DELETE FROM t WHERE n=1", "MERGE INTO t USING s ON t.n=s.n WHEN MATCHED THEN DELETE", "WITH n AS(SELECT 1) SELECT * FROM n", "SELECT 1; -- trailing comment"} {
		r, err := Build("trino", q)
		if err != nil || r != trinoPlanPrefix+q || !IsTrinoPlanRequest(r) {
			t.Fatal("native estimated request changed source", q, r, err)
		}
	}
	for _, q := range []string{"SELECT 1; INSERT INTO t VALUES(2)", "EXPLAIN ANALYZE SELECT 1", "ANALYZE t", "SET SESSION x=1", "START TRANSACTION", "CREATE TABLE t(n INT)", "/* only comment */", "SELECT '\x00'", "SELECT '\xff'", "SELECT `mysql`", "SELECT $$pg$$", strings.Repeat(" ", 1<<20) + "SELECT 1"} {
		if _, err := Build("trino", q); err == nil {
			t.Fatalf("unsupported/ambiguous source accepted: %q", q)
		}
	}
}

func TestTrinoPlanRejectsCyclesUnreachableFragmentsAndDisplayBudgets(t *testing.T) {
	makeNode := func(id, name string, descriptor map[string]string, children []any) map[string]any {
		return map[string]any{"id": id, "name": name, "descriptor": descriptor, "outputs": []any{}, "details": []any{}, "estimates": []any{}, "children": children}
	}
	encode := func(fragments map[string]any) string {
		t.Helper()
		raw, err := json.Marshal(fragments)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	cycle := map[string]any{
		"0": makeNode("0", "RemoteSource", map[string]string{"sourceFragmentIds": "[1]"}, []any{}),
		"1": makeNode("1", "RemoteSource", map[string]string{"sourceFragmentIds": "[0]"}, []any{}),
	}
	if _, err := Parse("trino", trinoPlanResult(encode(cycle))); err == nil {
		t.Fatal("cyclic fragment graph accepted")
	}
	unreachable := map[string]any{"0": makeNode("0", "Values", map[string]string{}, []any{}), "1": makeNode("1", "Values", map[string]string{}, []any{})}
	if _, err := Parse("trino", trinoPlanResult(encode(unreachable))); err == nil {
		t.Fatal("unreachable fragment treated as complete graph")
	}
	var deep any = makeNode("70", "Values", map[string]string{}, []any{})
	for i := 69; i >= 0; i-- {
		deep = makeNode(strconv.Itoa(i), "Project", map[string]string{}, []any{deep})
	}
	if _, err := Parse("trino", trinoPlanResult(encode(map[string]any{"0": deep}))); err == nil {
		t.Fatal("deep operator tree accepted")
	}
	children := make([]any, 0, 2300)
	for i := 0; i < 2300; i++ {
		n := makeNode(strconv.Itoa(i+1), "Values", map[string]string{}, []any{})
		n["estimates"] = []any{map[string]any{"outputRowCount": 1, "outputSizeInBytes": 5, "cpuCost": 0, "memoryCost": 0, "networkCost": 0}}
		children = append(children, n)
	}
	if _, err := Parse("trino", trinoPlanResult(encode(map[string]any{"0": makeNode("0", "Union", map[string]string{}, children)}))); err == nil {
		t.Fatal("estimate nodes exceeded display budget")
	}
}

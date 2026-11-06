package queryplan

import (
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func oceanBasePlanResult(raw string) contract.SQLResult {
	set := contract.ResultSet{}
	for _, line := range strings.Split(raw, "\n") {
		set.Rows = append(set.Rows, []contract.Cell{{Kind: contract.Text, Text: line}})
	}
	return contract.SQLResult{Sets: []contract.ResultSet{set}}
}

func TestOceanBaseMultilineNativePlanAndLimits(t *testing.T) {
	query, err := Build("oceanbase", "SELECT * FROM `records`")
	if err != nil || query != "EXPLAIN FORMAT=JSON SELECT * FROM `records`" {
		t.Fatal(query, err)
	}
	if _, err := Build("oceanbase", "SELECT 1; DELETE FROM records"); err == nil {
		t.Fatal("multiple statements accepted")
	}
	result := oceanBasePlanResult(`{
"ID":0,"OPERATOR":"SORT","EST.ROWS":2,"EST.TIME(us)":3,
"CHILD_2":{"ID":2,"OPERATOR":"TABLE FULL SCAN","NAME":"b"},
"CHILD_1":{"ID":1,"OPERATOR":"TABLE FULL SCAN","NAME":"a"}
}`)
	plan, err := Parse("oceanbase", result)
	if err != nil || plan.Engine != "oceanbase" || len(plan.Nodes) != 1 || len(plan.Nodes[0].Children) != 2 || !strings.HasPrefix(plan.Nodes[0].Children[0].Label, "1") || *plan.Nodes[0].Rows != 2 || plan.Nodes[0].Cost != nil || !strings.Contains(plan.Nodes[0].Detail, "EST.TIME(us)=3") {
		t.Fatal("native estimates or ordered children lost", err)
	}
	for _, raw := range []string{`{`, `{}`, `{"ID":null,"OPERATOR":"SORT"}`, `{"ID":0,"OPERATOR":"SORT","CHILD_1":{"ID":0,"OPERATOR":"SCAN"}}`, `{"ID":0,"OPERATOR":"SORT","CHILD_x":{}}`, `{"ID":0,"OPERATOR":"SORT","EST.ROWS":"unknown"}`} {
		if _, err := Parse("oceanbase", oceanBasePlanResult(raw)); err == nil {
			t.Fatal("invalid plan accepted")
		}
	}
	result.Truncated = true
	if _, err = Parse("oceanbase", result); err == nil {
		t.Fatal("truncated plan accepted")
	}
	result.Truncated = false
	result.Sets[0].Rows[0][0].Truncated = true
	if _, err = Parse("oceanbase", result); err == nil {
		t.Fatal("truncated line accepted")
	}
}

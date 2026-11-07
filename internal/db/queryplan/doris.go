package queryplan

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

func buildDoris(source string) (string, error) {
	const prefix = "EXPLAIN OPTIMIZED PLAN "
	if len(source) > (1<<20)-len(prefix) || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return "", errors.New("Doris execution plan SQL exceeds display bounds")
	}
	if err := sqltext.SingleMySQLStatement(source); err != nil {
		return "", err
	}
	tokens, err := sqltext.Tokens(source)
	if err != nil {
		return "", err
	}
	for _, token := range tokens {
		if token.Comment {
			continue
		}
		switch strings.ToUpper(token.Text) {
		case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE":
			return prefix + source, nil
		default:
			return "", contract.ErrUnsupported
		}
	}
	return "", contract.ErrUnsupported
}

var dorisOperator = regexp.MustCompile(`^(Physical[A-Za-z0-9_]+\[([0-9]+)\](?:@[0-9]*)?)(?: \( (.*) \))?$`)

func dorisEstimate(raw string) (*float64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil, errors.New("invalid Doris optimizer estimate")
	}
	return &v, nil
}

// The native serializer puts stats first, except for OlapScan where table is
// first. Later properties include expressions and even unbalanced runtime-filter
// notation; retain them verbatim without interpreting embedded stats-like text.
var dorisLeadingStats = regexp.MustCompile(`^stats=([^,]*)(?:, |$)`)
var dorisScanStats = regexp.MustCompile(`, stats=([^,]*), operativeSlots=`)
var dorisLazyFilters = regexp.MustCompile(`(?: RF[0-9]+)+$`)

func dorisRows(operator, detail string) (*float64, error) {
	match := dorisLeadingStats.FindStringSubmatch(detail)
	if strings.HasPrefix(operator, "PhysicalOlapScan[") && strings.HasPrefix(detail, "table=") {
		matches := dorisScanStats.FindAllStringSubmatch(detail, -1)
		// An ambiguous unquoted table/column name must not supply a metric.
		if len(matches) != 1 || strings.Count(detail, ", stats=") != 1 {
			return nil, errors.New("ambiguous Doris scan statistics")
		}
		match = matches[0]
	}
	if len(match) == 0 || match[1] == "null" {
		return nil, nil
	}
	return dorisEstimate(match[1])
}

func dorisNode(text string) (contract.PlanNode, string, error) {
	// These native lazy-materialization serializers have no independent ID.
	// The scan wrapper explicitly retains its original scan's statistics.
	if strings.HasPrefix(text, "PhysicalLazyMaterialize [Output= (") && strings.HasSuffix(text, ")]") {
		return contract.PlanNode{Label: "PhysicalLazyMaterialize", Detail: strings.TrimPrefix(text, "PhysicalLazyMaterialize ")}, "", nil
	}
	lazy := strings.HasPrefix(text, "PhysicalLazyMaterializeOlapScan[") && strings.HasSuffix(text, "]")
	inner := text
	if lazy {
		inner = strings.TrimSuffix(strings.TrimPrefix(text, "PhysicalLazyMaterializeOlapScan["), "]")
		inner = dorisLazyFilters.ReplaceAllString(inner, "")
		if !strings.HasPrefix(inner, "PhysicalOlapScan[") {
			return contract.PlanNode{}, "", errors.New("invalid Doris lazy scan")
		}
	}
	m := dorisOperator.FindStringSubmatch(inner)
	if len(m) != 4 {
		return contract.PlanNode{}, "", errors.New("unsupported Doris physical operator")
	}
	node := contract.PlanNode{Label: m[1], Detail: m[3]}
	var err error
	node.Rows, err = dorisRows(m[1], m[3])
	if lazy {
		node.Label = strings.Replace(m[1], "PhysicalOlapScan[", "PhysicalLazyMaterializeOlapScan[", 1)
		node.Detail = text
	}
	return node, m[2], err
}

// Doris 4.1.3 OPTIMIZED PLAN is a Nereids physical tree, preceded by the
// complete optimizer cost. Keep that cost on a separate plan envelope; it is
// not an operator cost. The original SQL result remains available in the UI.
func parseDoris(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	fail := func() (*contract.ExecutionPlan, error) {
		return nil, errors.New("invalid, truncated or unsupported Doris Nereids plan; see original result")
	}
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Truncation) != 0 || len(sets[0].Rows) < 2 || len(sets[0].Rows) > 10000 {
		return fail()
	}
	type item struct {
		node       contract.PlanNode
		children   []int
		more       bool
		finalChild bool
	}
	var items []item
	var path []int
	seen := map[string]bool{}
	used := 0
	var cost *float64
	var header string
	for i, row := range sets[0].Rows {
		if len(row) != 1 || row[0].Truncated || row[0].Kind != contract.Text || len(row[0].Bytes) != 0 {
			return fail()
		}
		raw := row[0].Text
		used += len(raw)
		if used > 8<<20 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n\x00\t") {
			return fail()
		}
		if i == 0 {
			if !strings.HasPrefix(raw, "cost = ") {
				return fail()
			}
			var err error
			cost, err = dorisEstimate(strings.TrimPrefix(raw, "cost = "))
			if err != nil {
				return fail()
			}
			header = raw
			continue
		}
		depth, more := 0, false
		text := raw
		if i > 1 {
			for strings.HasPrefix(text, "   ") || strings.HasPrefix(text, "|  ") {
				if depth+1 >= len(path) || depth >= 63 {
					return fail()
				}
				want := "   "
				if items[path[depth+1]].more {
					want = "|  "
				}
				if text[:3] != want {
					return fail()
				}
				depth++
				text = text[3:]
			}
			if !strings.HasPrefix(text, "+--") && !strings.HasPrefix(text, "|--") {
				return fail()
			}
			more = text[0] == '|'
			depth++
			text = text[3:]
			if depth > len(path) || items[path[depth-1]].finalChild {
				return fail()
			}
		}
		node, id, err := dorisNode(text)
		if err != nil || id != "" && seen[id] {
			return fail()
		}
		if id != "" {
			seen[id] = true
		}
		index := len(items)
		items = append(items, item{node: node, more: more})
		if depth > 0 {
			parent := &items[path[depth-1]]
			parent.children = append(parent.children, index)
			parent.finalChild = !more
		}
		path = append(path[:depth], index)
	}
	if len(items) == 0 {
		return fail()
	}
	for _, item := range items {
		if len(item.children) > 0 && !item.finalChild {
			return fail()
		}
	}
	var build func(int) contract.PlanNode
	build = func(index int) contract.PlanNode {
		n := items[index].node
		for _, child := range items[index].children {
			n.Children = append(n.Children, build(child))
		}
		return n
	}
	return &contract.ExecutionPlan{Engine: "doris", Nodes: []contract.PlanNode{{Label: "Nereids optimized plan", Detail: header, Cost: cost, Children: []contract.PlanNode{build(0)}}}}, nil
}

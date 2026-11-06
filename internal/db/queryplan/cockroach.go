package queryplan

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

var cockroachRows = regexp.MustCompile(`(?:^|\[|, *)rows=([0-9.eE+\-]+)(?:,|\])`)

// OPT VERBOSE is the native optimizer tree. Property branches describe their
// owning operator; rows and optimizer cost are retained without executing SQL.
func parseCockroach(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Truncation) > 0 || len(sets[0].Rows) == 0 || len(sets[0].Rows) > 10000 {
		return nil, errors.New("empty, truncated or oversized CockroachDB plan")
	}
	type item struct {
		node     contract.PlanNode
		children []int
	}
	var items []item
	var roots []int
	stack := map[int]int{}
	used := 0
	fail := func() (*contract.ExecutionPlan, error) { return nil, errors.New("invalid CockroachDB optimizer plan") }
	for _, row := range sets[0].Rows {
		if len(row) != 1 || row[0].Truncated || row[0].Kind == contract.Null {
			return fail()
		}
		raw := row[0].Text
		used += len(raw)
		if used > 4<<20 || !utf8.ValidString(raw) {
			return fail()
		}
		if strings.Contains(raw, "\n") {
			return fail()
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		depth := 0
		text := strings.TrimSpace(raw)
		at := strings.Index(raw, "├── ")
		if at < 0 {
			at = strings.Index(raw, "└── ")
		}
		if at >= 0 {
			prefix := raw[:at]
			position := utf8.RuneCountInString(prefix)
			if position < 1 || (position-1)%5 != 0 || strings.Trim(prefix, " │") != "" {
				return fail()
			}
			depth = 1 + (position-1)/5
			text = strings.TrimSpace(raw[at+len("├── "):])
		}
		if text == "" || depth > 64 {
			return fail()
		}
		parent, hasParent := stack[depth-1]
		if depth > 0 && !hasParent {
			return fail()
		}
		property := text == "immutable" || text == "stable" || text == "volatile" || text == "side-effects"
		for _, key := range []string{"columns", "stats", "cost", "key", "fd", "prune", "ordering", "distribution", "not-null", "outer", "cardinality", "constraint", "constraints", "lock", "limit", "provided", "required", "presentation"} {
			if strings.HasPrefix(text, key+": ") {
				property = true
				break
			}
		}
		// A scalar expression can contain colon type IDs but no colon-space;
		// an attribute branch always belongs to an existing operator.
		if property {
			if depth == 0 {
				return fail()
			}
			n := &items[parent].node
			if n.Detail != "" {
				n.Detail += "\n"
			}
			n.Detail += text
			parse := func(raw string) (*float64, error) {
				v, e := strconv.ParseFloat(raw, 64)
				if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					return nil, errors.New("invalid optimizer estimate")
				}
				return &v, nil
			}
			if strings.HasPrefix(text, "cost: ") {
				v, e := parse(strings.TrimPrefix(text, "cost: "))
				if e != nil || n.Cost != nil {
					return fail()
				}
				n.Cost = v
			}
			if strings.HasPrefix(text, "stats: ") {
				m := cockroachRows.FindStringSubmatch(text)
				if len(m) != 2 || n.Rows != nil {
					return fail()
				}
				v, e := parse(m[1])
				if e != nil {
					return fail()
				}
				n.Rows = v
			}
			continue
		}
		index := len(items)
		items = append(items, item{node: contract.PlanNode{Label: text}})
		if depth == 0 {
			roots = append(roots, index)
		} else {
			items[parent].children = append(items[parent].children, index)
		}
		for d := range stack {
			if d >= depth {
				delete(stack, d)
			}
		}
		stack[depth] = index
	}
	if len(roots) != 1 {
		return fail()
	}
	var build func(int) contract.PlanNode
	build = func(i int) contract.PlanNode {
		n := items[i].node
		for _, c := range items[i].children {
			n.Children = append(n.Children, build(c))
		}
		return n
	}
	return &contract.ExecutionPlan{Engine: "cockroachdb", Nodes: []contract.PlanNode{build(roots[0])}}, nil
}

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

func buildStarRocks(source string) (string, error) {
	const prefix = "EXPLAIN COSTS "
	if len(source) > (1<<20)-len(prefix) || !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return "", errors.New("StarRocks execution plan SQL exceeds display bounds")
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

var starRocksFragment = regexp.MustCompile(`^PLAN FRAGMENT ([0-9]+)\(F([0-9]+)\)$`)
var starRocksOperator = regexp.MustCompile(`^([0-9]+):([A-Za-z][A-Za-z0-9_ ()/-]*)$`)

type starRocksItem struct {
	node                          contract.PlanNode
	prefix                        string
	children                      []int
	depth                         int
	traverses, mainChild, hasRows bool
	style                         string
	details                       []string
}

type starRocksPart struct {
	label     string
	header    []string
	items     []starRocksItem
	path      []int
	partition bool
	exchanges []string
}

func starRocksID(raw string) (string, error) {
	n, err := strconv.ParseUint(raw, 10, 31)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(n, 10), nil
}

// COSTS prints whole-plan CPU and memory estimates separately. Neither is an
// operator cost, an elapsed time, nor a measured allocation in bytes.
func starRocksCost(raw string) (*float64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil, errors.New("invalid StarRocks optimizer cost")
	}
	return &v, nil
}

func starRocksCardinality(raw string) (*float64, error) {
	// The native serializer emits a Java long; -1 represents an unknown value.
	if raw == "-1" {
		return nil, nil
	}
	if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return nil, errors.New("invalid StarRocks cardinality")
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	n := float64(v)
	return &n, nil
}

// StarRocks 4.1.7 PlanNode.getCostExplain prints children 1..N with a
// "|----" branch, then child 0 with the parent's prefix. Prefix width alone
// is therefore not tree depth. Restore child 0 first in the logical tree.
func (p *starRocksPart) addOperator(raw string, seen map[string]bool, exchanges map[string]string) error {
	if !strings.HasPrefix(raw, "  ") {
		return errors.New("invalid StarRocks operator prefix")
	}
	text, level := raw[2:], 0
	for strings.HasPrefix(text, "|    ") {
		level++
		text = text[5:]
	}
	side := strings.HasPrefix(text, "|----")
	if side {
		text = text[5:]
	}
	match := starRocksOperator.FindStringSubmatch(text)
	if match == nil || level > 63 {
		return errors.New("invalid StarRocks operator")
	}
	id, err := starRocksID(match[1])
	if err != nil || seen[id] {
		return errors.New("duplicate or invalid StarRocks operator ID")
	}
	index, depth := len(p.items), 0
	parent := -1
	if index == 0 {
		if level != 0 || side {
			return errors.New("orphan StarRocks root")
		}
	} else {
		if level >= len(p.path) {
			return errors.New("orphan StarRocks branch")
		}
		parent = p.path[level]
		item := &p.items[parent]
		if !item.traverses || item.mainChild {
			return errors.New("StarRocks child follows a closed branch")
		}
		depth = item.depth + 1
		if depth > 63 {
			return errors.New("StarRocks operator tree exceeds display depth")
		}
		if side {
			item.children = append(item.children, index)
		} else {
			item.mainChild = true
			item.children = append([]int{index}, item.children...)
		}
	}
	seen[id] = true
	if strings.Contains(match[2], "EXCHANGE") {
		exchanges[id] = p.label
	}
	if side {
		level++
	}
	prefix := "  " + strings.Repeat("|    ", level)
	p.items = append(p.items, starRocksItem{node: contract.PlanNode{Label: match[0]}, prefix: prefix, depth: depth})
	p.path = append(p.path[:level], index)
	return nil
}

func (p *starRocksPart) detail(raw string) error {
	if len(p.items) == 0 {
		if !strings.HasPrefix(raw, "  ") {
			return errors.New("invalid StarRocks fragment header")
		}
		if strings.HasPrefix(raw, "  Input Partition: ") {
			if p.partition || strings.TrimSpace(strings.TrimPrefix(raw, "  Input Partition: ")) == "" {
				return errors.New("duplicate or missing StarRocks input partition")
			}
			p.partition = true
		}
		if strings.HasPrefix(raw, "  OutPut Exchange Id: ") {
			id, err := starRocksID(strings.TrimPrefix(raw, "  OutPut Exchange Id: "))
			if err != nil {
				return err
			}
			p.exchanges = append(p.exchanges, id)
		}
		p.header = append(p.header, raw)
		return nil
	}
	item := &p.items[len(p.items)-1]
	if !strings.HasPrefix(raw, item.prefix) {
		return errors.New("invalid StarRocks detail prefix")
	}
	text := strings.TrimPrefix(raw, item.prefix)
	if len(text) < 3 || text[:3] != "|  " && text[:3] != "   " {
		return errors.New("invalid StarRocks detail layout")
	}
	style := text[:3]
	if item.style != "" && item.style != style {
		return errors.New("ambiguous StarRocks branch layout")
	}
	item.style, item.traverses = style, style == "|  "
	text = text[3:]
	if strings.HasPrefix(text, "cardinality: ") {
		if item.hasRows {
			return errors.New("duplicate StarRocks cardinality")
		}
		rows, err := starRocksCardinality(strings.TrimPrefix(text, "cardinality: "))
		if err != nil {
			return err
		}
		item.hasRows, item.node.Rows = true, rows
	}
	item.details = append(item.details, text)
	return nil
}

func (p *starRocksPart) finish() (contract.PlanNode, error) {
	if !p.partition || len(p.items) == 0 {
		return contract.PlanNode{}, errors.New("incomplete StarRocks fragment")
	}
	for _, item := range p.items {
		if !item.hasRows || item.traverses != item.mainChild {
			return contract.PlanNode{}, errors.New("incomplete StarRocks operator tree")
		}
	}
	var build func(int) contract.PlanNode
	build = func(index int) contract.PlanNode {
		item := &p.items[index]
		node := item.node
		node.Detail = strings.Join(item.details, "\n")
		for _, child := range item.children {
			node.Children = append(node.Children, build(child))
		}
		return node
	}
	return contract.PlanNode{Label: p.label, Detail: strings.Join(p.header, "\n"), Children: []contract.PlanNode{build(0)}}, nil
}

func parseStarRocks(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	fail := func() (*contract.ExecutionPlan, error) {
		return nil, errors.New("invalid, truncated or unsupported StarRocks COSTS plan; see original result")
	}
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Columns) != 1 || len(sets[0].Truncation) != 0 || len(sets[0].Rows) < 4 || len(sets[0].Rows) > 10000 {
		return fail()
	}
	var lines []string
	used := 0
	for _, row := range sets[0].Rows {
		if len(row) != 1 || row[0].Truncated || row[0].Kind != contract.Text || len(row[0].Bytes) != 0 {
			return fail()
		}
		raw := row[0].Text
		used += len(raw)
		if used > 8<<20 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n\x00\t") {
			return fail()
		}
		lines = append(lines, raw)
	}
	index := 0
	fe := ""
	if lines[0] == "EXECUTE IN FE" {
		fe = lines[0] + "\n"
		index++
	}
	if len(lines) < index+3 || lines[index] != "PLAN COST" || !strings.HasPrefix(lines[index+1], "  CPU: ") || !strings.HasPrefix(lines[index+2], "  Memory: ") {
		return fail()
	}
	cpu, err := starRocksCost(strings.TrimPrefix(lines[index+1], "  CPU: "))
	if err != nil {
		return fail()
	}
	memory, err := starRocksCost(strings.TrimPrefix(lines[index+2], "  Memory: "))
	if err != nil {
		return fail()
	}
	cost := contract.PlanNode{Label: "PLAN COST", Detail: strings.Join(lines[index:index+3], "\n"), Children: []contract.PlanNode{{Label: "CPU", Cost: cpu}, {Label: "Memory", Cost: memory}}}
	root := contract.PlanNode{Label: "StarRocks estimated plan", Detail: fe, Children: []contract.PlanNode{cost}}
	seen, fragments := map[string]bool{}, map[string]bool{}
	exchanges := map[string]string{}
	produced := map[string]bool{}
	var outgoing []struct{ from, exchange string }
	var part *starRocksPart
	finish := func() error {
		if part == nil {
			return nil
		}
		node, err := part.finish()
		if err != nil {
			return err
		}
		root.Children = append(root.Children, node)
		for _, id := range part.exchanges {
			outgoing = append(outgoing, struct{ from, exchange string }{part.label, id})
		}
		return nil
	}
	for _, raw := range lines[index+3:] {
		if strings.Trim(raw, " |") == "" {
			continue
		}
		if match := starRocksFragment.FindStringSubmatch(raw); match != nil {
			ordinal, e := strconv.Atoi(match[1])
			id, ie := starRocksID(match[2])
			if e != nil || ie != nil || ordinal != len(fragments) || fragments[id] || finish() != nil {
				return fail()
			}
			fragments[id] = true
			part = &starRocksPart{label: raw}
			continue
		}
		if part == nil {
			return fail()
		}
		text := strings.TrimPrefix(raw, "  ")
		for strings.HasPrefix(text, "|    ") {
			text = text[5:]
		}
		text = strings.TrimPrefix(text, "|----")
		if starRocksOperator.MatchString(text) {
			if err := part.addOperator(raw, seen, exchanges); err != nil {
				return fail()
			}
		} else if err := part.detail(raw); err != nil {
			return fail()
		}
	}
	if part == nil || finish() != nil {
		return fail()
	}
	graph := map[string][]string{}
	for _, edge := range outgoing {
		destination, ok := exchanges[edge.exchange]
		if !ok || destination == edge.from {
			return fail()
		}
		produced[edge.exchange] = true
		graph[edge.from] = append(graph[edge.from], destination)
	}
	for id := range exchanges {
		if !produced[id] {
			return fail()
		}
	}
	// Preserve fragments as separate trees (including multicast DAGs). Validate
	// references in both directions so missing producer fragments cannot look
	// like a complete plan when a transport omitted a truncation marker.
	visits := map[string]int{}
	var acyclic func(string, int) bool
	acyclic = func(from string, depth int) bool {
		if depth > 63 || visits[from] == 1 {
			return false
		}
		if visits[from] == 2 {
			return true
		}
		visits[from] = 1
		for _, destination := range graph[from] {
			if !acyclic(destination, depth+1) {
				return false
			}
		}
		visits[from] = 2
		return true
	}
	for from := range graph {
		if !acyclic(from, 0) {
			return fail()
		}
	}
	return &contract.ExecutionPlan{Engine: "starrocks", Nodes: []contract.PlanNode{root}}, nil
}

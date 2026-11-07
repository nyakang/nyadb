package queryplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
)

const trinoPlanPrefix = "EXPLAIN (TYPE DISTRIBUTED, FORMAT JSON) "

func buildTrino(source string) (string, error) {
	if len(source) > (1<<20)-len(trinoPlanPrefix) {
		return "", errors.New("Trino plan SQL exceeds 1 MiB")
	}
	if err := sqltext.SingleTrinoStatement(source); err != nil {
		return "", err
	}
	ts, err := sqltext.TrinoTokens(source)
	if err != nil {
		return "", err
	}
	for _, t := range ts {
		if t.Comment {
			continue
		}
		if !t.Quoted {
			switch strings.ToUpper(t.Text) {
			case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "MERGE":
				return trinoPlanPrefix + source, nil
			}
		}
		break
	}
	return "", contract.ErrUnsupported
}

// IsTrinoPlanRequest identifies the native, non-executing JSON plan envelope.
// Its single full result cell has a separate bounded cache from ordinary SQL.
func IsTrinoPlanRequest(source string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(source)), trinoPlanPrefix)
}

type trinoRenderedNode struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Descriptor map[string]string `json:"descriptor"`
	Outputs    []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"outputs"`
	Details   []string                     `json:"details"`
	Estimates []map[string]json.RawMessage `json:"estimates"`
	Children  []json.RawMessage            `json:"children"`
}

// Encoding/json otherwise accepts duplicate object members by replacing the
// earlier value. A plan with duplicate fragments, IDs or metrics is ambiguous.
func trinoJSONUnique(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) error
	read = func(depth int) error {
		if depth > 128 {
			return errors.New("Trino JSON depth exceeded")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("duplicate Trino JSON member")
					}
					seen[name] = true
					if err = read(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err := read(depth + 1); err != nil {
						return err
					}
				}
			default:
				return errors.New("invalid Trino JSON delimiter")
			}
			if _, err = d.Token(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := read(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("multiple Trino JSON roots")
	}
	return nil
}

func trinoID(s string) bool {
	if s == "" || len(s) > 10 || len(s) > 1 && s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(s, 10, 31)
	return err == nil
}

func trinoMetric(raw json.RawMessage) (*float64, error) {
	if string(raw) == `"NaN"` || string(raw) == `"Infinity"` {
		return nil, nil
	}
	if len(raw) == 0 || raw[0] == '"' || string(raw) == "null" {
		return nil, errors.New("invalid Trino estimate")
	}
	v, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, errors.New("invalid Trino estimate")
	}
	return &v, nil
}

func parseTrino(result contract.SQLResult) (*contract.ExecutionPlan, error) {
	fail := func() (*contract.ExecutionPlan, error) {
		return nil, errors.New("invalid, truncated or unsupported Trino distributed JSON plan; see original result")
	}
	sets := result.ResultSets()
	if result.Truncated || len(sets) != 1 || len(sets[0].Columns) != 1 || sets[0].Columns[0].Name != "Query Plan" || len(sets[0].Truncation) > 0 || len(sets[0].Rows) != 1 || len(sets[0].Rows[0]) != 1 {
		return fail()
	}
	cell := sets[0].Rows[0][0]
	if cell.Kind != contract.Text || cell.Truncated || len(cell.Bytes) != 0 || len(cell.Text) == 0 || len(cell.Text) > 1<<20 || !utf8.ValidString(cell.Text) || !json.Valid([]byte(cell.Text)) || trinoJSONUnique([]byte(cell.Text)) != nil {
		return fail()
	}
	var fragments map[string]json.RawMessage
	if json.Unmarshal([]byte(cell.Text), &fragments) != nil || len(fragments) == 0 || len(fragments) > 512 || fragments["0"] == nil {
		return fail()
	}
	ids := make([]string, 0, len(fragments))
	for id := range fragments {
		if !trinoID(id) {
			return fail()
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { a, _ := strconv.Atoi(ids[i]); b, _ := strconv.Atoi(ids[j]); return a < b })
	seen := map[string]bool{}
	graph := map[string][]string{}
	count := 1 + len(ids)
	var node func(json.RawMessage, string, int) (contract.PlanNode, error)
	node = func(raw json.RawMessage, fragment string, depth int) (contract.PlanNode, error) {
		count++
		if depth > 59 || count > 10000 {
			return contract.PlanNode{}, errors.New("Trino plan exceeds display bounds")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
			return contract.PlanNode{}, errors.New("invalid Trino node fields")
		}
		for _, key := range []string{"id", "name", "descriptor", "outputs", "details", "estimates", "children"} {
			if fields[key] == nil || string(fields[key]) == "null" {
				return contract.PlanNode{}, errors.New("missing Trino node field")
			}
		}
		var n trinoRenderedNode
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&n) != nil || !trinoID(n.ID) || seen[n.ID] || n.Name == "" || len(n.Name) > 1024 || strings.ContainsAny(n.Name, "\r\n\x00") || len(n.Outputs) > 512 || len(n.Descriptor) > 64 || len(n.Details) > 512 || len(n.Estimates) > 64 {
			return contract.PlanNode{}, errors.New("invalid Trino node")
		}
		seen[n.ID] = true
		fusedStages := map[string]int{"ScanFilter": 2, "ScanProject": 2, "FilterProject": 2, "ScanFilterProject": 3}
		if stages := fusedStages[n.Name]; stages != 0 && len(n.Estimates) != 0 && len(n.Estimates) != stages {
			return contract.PlanNode{}, errors.New("incomplete Trino fused estimates")
		}
		out := contract.PlanNode{Label: n.ID + ":" + n.Name}
		keys := make([]string, 0, len(n.Descriptor))
		for key := range n.Descriptor {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var detail []string
		for _, key := range keys {
			detail = append(detail, key+": "+n.Descriptor[key])
		}
		for _, s := range n.Outputs {
			if s.Name == "" || s.Type == "" {
				return out, errors.New("invalid Trino output")
			}
			detail = append(detail, "output: "+s.Name+" "+s.Type)
		}
		detail = append(detail, n.Details...)
		out.Detail = strings.Join(detail, "\n")
		if len(out.Detail) > 64<<10 || strings.ContainsRune(out.Detail, 0) {
			return out, errors.New("Trino node detail exceeds display bounds")
		}
		if source, ok := n.Descriptor["sourceFragmentIds"]; ok {
			if n.Name != "RemoteSource" && n.Name != "RemoteMerge" || len(n.Children) != 0 || len(n.Estimates) != 0 {
				return out, errors.New("invalid Trino remote node")
			}
			var refs []json.RawMessage
			if json.Unmarshal([]byte(source), &refs) != nil || len(refs) == 0 || len(refs) > 512 {
				return out, errors.New("invalid Trino fragment reference")
			}
			local := map[string]bool{}
			for _, v := range refs {
				ref := string(v)
				if !trinoID(ref) || fragments[ref] == nil || ref == fragment || local[ref] {
					return out, errors.New("missing, duplicate or cyclic Trino fragment")
				}
				local[ref] = true
				graph[fragment] = append(graph[fragment], ref)
			}
		} else if n.Name == "RemoteSource" || n.Name == "RemoteMerge" {
			return out, errors.New("missing Trino source fragments")
		}
		for i, e := range n.Estimates {
			if len(e) != 5 {
				return out, errors.New("invalid Trino estimate fields")
			}
			values := make([]*float64, 5)
			labels := []string{"outputRowCount", "outputSizeInBytes", "cpuCost", "memoryCost", "networkCost"}
			for j, key := range labels {
				v, err := trinoMetric(e[key])
				if err != nil {
					return out, err
				}
				values[j] = v
			}
			// JsonRenderer retains allNodes in scan/filter/project order. The
			// last estimate is the fused operator's output; keep intermediate
			// stages independently rather than summing unlike optimizer costs.
			if len(n.Estimates) == 1 {
				out.Rows = values[0]
			}
			fused := fusedStages[n.Name] != 0
			if fused && i == len(n.Estimates)-1 {
				out.Rows = values[0]
			}
			encoded, _ := json.Marshal(e)
			est := contract.PlanNode{Label: fmt.Sprintf("Estimate %d", i+1), Rows: values[0], Detail: string(encoded)}
			for j, key := range []string{"CPU", "Memory", "Network"} {
				est.Children = append(est.Children, contract.PlanNode{Label: key, Cost: values[j+2]})
			}
			out.Children = append(out.Children, est)
			count += 4
			if count > 10000 {
				return out, errors.New("Trino rendered plan exceeds node budget")
			}
		}
		for _, child := range n.Children {
			c, err := node(child, fragment, depth+1)
			if err != nil {
				return out, err
			}
			out.Children = append(out.Children, c)
		}
		return out, nil
	}
	root := contract.PlanNode{Label: "Trino estimated plan"}
	for _, id := range ids {
		n, err := node(fragments[id], id, 0)
		if err != nil {
			return fail()
		}
		root.Children = append(root.Children, contract.PlanNode{Label: "Fragment " + id, Children: []contract.PlanNode{n}})
	}
	visits := map[string]int{}
	var acyclic func(string, int) bool
	acyclic = func(id string, depth int) bool {
		if depth > 60 || visits[id] == 1 {
			return false
		}
		if visits[id] == 2 {
			return true
		}
		visits[id] = 1
		for _, ref := range graph[id] {
			if !acyclic(ref, depth+1) {
				return false
			}
		}
		visits[id] = 2
		return true
	}
	if !acyclic("0", 0) || len(visits) != len(ids) {
		return fail()
	}
	return &contract.ExecutionPlan{Engine: "trino", Nodes: []contract.PlanNode{root}}, nil
}

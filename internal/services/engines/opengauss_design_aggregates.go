package engines

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func gaussDesignExpectedAggregates(expected map[string]map[string]any, before, probe, actual schemaddl.OpenGaussDesignGraph, original contract.SchemaDesignSnapshot, desired schemaddl.Table, changed, rebound map[string]bool) error {
	affected := map[string]bool{}
	for id := range changed {
		affected[id] = true
	}
	// Implicit RI triggers inherit the explicitly changed FK's identity.
	for _, graph := range []schemaddl.OpenGaussDesignGraph{before, probe} {
		for _, object := range graph.Objects {
			for id := range changed {
				if strings.HasPrefix(object, "@internal-trigger:"+id+":") {
					affected[object] = true
				}
			}
		}
	}
	oldColumns := map[string]schemaddl.Clause{}
	for _, c := range original.Schema.Table.Columns() {
		oldColumns[c.ID] = c
	}
	nextColumns := map[string]schemaddl.Clause{}
	changedColumns := map[string]bool{}
	for _, c := range desired.Columns() {
		nextColumns[c.ID] = c
		old, ok := oldColumns[c.ID]
		if !ok || old.SQL != c.SQL {
			changedColumns["@column:"+c.ID] = true
			affected["@default:@column:"+c.ID] = true
			affected["@default:@table:reldeltarelid:@column:"+c.ID] = true
		}
		if !ok {
			affected["@sequence:@column:"+c.ID] = true
		}
	}
	for id := range oldColumns {
		if _, ok := nextColumns[id]; !ok {
			changedColumns["@column:"+id] = true
			affected["@default:@column:"+id] = true
			affected["@default:@table:reldeltarelid:@column:"+id] = true
			affected["@sequence:@column:"+id] = true
		}
	}
	for key := range expected {
		for id := range affected {
			if strings.Contains(key, ":"+id) {
				delete(expected, key)
			}
		}
	}
	for key, row := range probe.Records {
		for id := range affected {
			if strings.Contains(key, ":"+id) {
				expected[key] = gaussDesignCloneRecord(row)
			}
		}
	}
	commentTargets := map[string]bool{}
	commentTarget := func(object string, sub string) string { return object + ":" + sub }
	if !original.Schema.Exists || schemaddl.Signature(original.Schema.Table.OpenGauss.Comment) != schemaddl.Signature(desired.OpenGauss.Comment) {
		commentTargets[commentTarget("@table", "0")] = true
	}
	for _, c := range desired.Clauses {
		object := "@constraint:" + c.ID
		sub := "0"
		if c.Kind == "column" {
			object = "@table"
			sub = "@column:" + c.ID
		}
		var prior *string
		for _, old := range original.Schema.Table.Clauses {
			if old.ID == c.ID && old.OpenGauss != nil {
				prior = old.OpenGauss.Comment
			}
		}
		var next *string
		if c.OpenGauss != nil {
			next = c.OpenGauss.Comment
		}
		if schemaddl.Signature(prior) != schemaddl.Signature(next) || affected[object] {
			commentTargets[commentTarget(object, sub)] = true
		}
	}
	for id := range oldColumns {
		if _, ok := nextColumns[id]; !ok {
			commentTargets[commentTarget("@table", "@column:"+id)] = true
		}
	}
	for _, d := range desired.Definitions {
		object := "@" + d.Kind + ":" + d.Name
		var prior *string
		for _, old := range original.Schema.Table.Definitions {
			if d.Kind == old.Kind && d.Name == old.Name && old.OpenGauss != nil {
				prior = old.OpenGauss.Comment
			}
		}
		var next *string
		if d.OpenGauss != nil {
			next = d.OpenGauss.Comment
		}
		if schemaddl.Signature(prior) != schemaddl.Signature(next) || affected[object] {
			commentTargets[commentTarget(object, "0")] = true
		}
	}
	for _, kind := range []string{"dependencies", "shared_dependencies", "comments"} {
		key := kind + ":@table"
		if !original.Schema.Exists {
			if row := probe.Records[key]; row != nil {
				expected[key] = gaussDesignCloneRecord(row)
			}
			continue
		}
		row := before.Records[key]
		if row == nil {
			return schemaddl.ErrConflict
		}
		expected[key] = gaussDesignCloneRecord(row)
		selected := func(raw any) bool {
			x, ok := raw.(map[string]any)
			if !ok {
				return true
			}
			if kind == "comments" {
				object := fmt.Sprint(x["objoid"])
				sub := fmt.Sprint(x["objsubid"])
				return commentTargets[commentTarget(object, sub)] || affected[object]
			}
			object := fmt.Sprint(x["objid"])
			reference := fmt.Sprint(x["refobjid"])
			if affected[object] || affected[reference] {
				return true
			}
			if reference == "@table" && strings.HasPrefix(object, "@sequence:@column:") {
				id := strings.TrimPrefix(object, "@sequence:@column:")
				_, existed := oldColumns[id]
				_, retained := nextColumns[id]
				if existed && retained {
					// The independent CREATE references this retained sequence;
					// its original ownership edge stays on the original table.
					return false
				}
			}
			if rebound[object] || rebound[reference] {
				return true
			}
			if (object == "@table" || object == "@table:reldeltarelid") && changedColumns[fmt.Sprint(x["objsubid"])] {
				return true
			}
			if (reference == "@table" || reference == "@table:reldeltarelid") && changedColumns[fmt.Sprint(x["refobjsubid"])] {
				return true
			}
			return false
		}
		var rows []any
		if values, ok := row["rows"].([]any); ok {
			for _, value := range values {
				if !selected(value) {
					rows = append(rows, value)
				}
			}
		}
		if prototype := probe.Records[key]; prototype != nil {
			if values, ok := prototype["rows"].([]any); ok {
				for _, value := range values {
					if selected(value) {
						rows = append(rows, value)
					}
				}
			}
		}
		if len(rows) == 0 {
			expected[key]["rows"] = nil
		} else {
			sort.Slice(rows, func(i, j int) bool { return schemaddl.Signature(rows[i]) < schemaddl.Signature(rows[j]) })
			expected[key]["rows"] = rows
		}
		if values, ok := actual.Records[key]["rows"].([]any); ok {
			sort.Slice(values, func(i, j int) bool { return schemaddl.Signature(values[i]) < schemaddl.Signature(values[j]) })
		}
	}
	return nil
}

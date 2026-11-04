package contract

import (
	"errors"
	"strings"
)

// FilterExpression preserves grouping. Exactly one of Rule or Children is
// present. Leaves use Op "rule"; groups use "and" or "or".
type FilterExpression[T any] struct {
	Op       string
	Rule     *T
	Children []FilterExpression[T]
}

func (e *FilterExpression[T]) Clone(clone func(T) T) *FilterExpression[T] {
	if e == nil {
		return nil
	}
	out := &FilterExpression[T]{Op: e.Op}
	if e.Rule != nil {
		v := clone(*e.Rule)
		out.Rule = &v
	}
	for i := range e.Children {
		out.Children = append(out.Children, *e.Children[i].Clone(clone))
	}
	return out
}

func (e *FilterExpression[T]) Rules(maxRules, maxGroups int) ([]T, error) {
	var rules []T
	var walk func(*FilterExpression[T], int) error
	walk = func(node *FilterExpression[T], groups int) error {
		if node == nil {
			return nil
		}
		if node.Rule != nil {
			if len(node.Children) > 0 || node.Op != "rule" {
				return errors.New("invalid filter leaf")
			}
			rules = append(rules, *node.Rule)
			if len(rules) > maxRules {
				return errors.New("too many filter rules")
			}
			return nil
		}
		if node.Op != "and" && node.Op != "or" || len(node.Children) == 0 || groups >= maxGroups {
			return errors.New("invalid or empty filter group")
		}
		for i := range node.Children {
			if err := walk(&node.Children[i], groups+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(e, 0)
	return rules, err
}

func AndFilters[T any](rules []T) *FilterExpression[T] {
	if len(rules) == 0 {
		return nil
	}
	e := &FilterExpression[T]{Op: "and"}
	for _, rule := range rules {
		v := rule
		e.Children = append(e.Children, FilterExpression[T]{Op: "rule", Rule: &v})
	}
	return e
}

// CompileFilter inserts grouping only. Adapter-owned leaf functions quote
// validated identifiers and append bound parameters in traversal order.
func CompileFilter[T any](e *FilterExpression[T], leaf func(T) (string, error)) (string, error) {
	if e == nil {
		return "", nil
	}
	if e.Rule != nil {
		return leaf(*e.Rule)
	}
	parts := make([]string, 0, len(e.Children))
	for i := range e.Children {
		part, err := CompileFilter(&e.Children[i], leaf)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	return "(" + strings.Join(parts, " "+strings.ToUpper(e.Op)+" ") + ")", nil
}

func EffectiveDataExpression(o DataOptions) (*FilterExpression[DataFilterRule], error) {
	if o.Condition != nil {
		if len(o.Filters) > 0 || o.FilterMode != "" || o.FilterColumn != "" || o.FilterValue != "" {
			return nil, errors.New("ambiguous filter representations")
		}
		if _, err := o.Condition.Rules(DataMaxFilterRules, 3); err != nil {
			return nil, err
		}
		return o.Condition, nil
	}
	if len(o.Filters) > 0 && o.FilterMode != "" {
		return nil, errors.New("ambiguous filter representations")
	}
	rules := append([]DataFilterRule(nil), o.Filters...)
	if o.FilterMode != "" {
		rules = append(rules, DataFilterRule{o.FilterColumn, o.FilterMode, o.FilterValue})
	}
	if len(rules) > DataMaxFilterRules {
		return nil, errors.New("too many filter rules")
	}
	return AndFilters(rules), nil
}

func EffectivePageExpression(r PageRequest) (*FilterExpression[Filter], error) {
	if r.Condition != nil {
		if len(r.Filters) > 0 {
			return nil, errors.New("ambiguous filter representations")
		}
		if _, err := r.Condition.Rules(DataMaxFilterRules, 3); err != nil {
			return nil, err
		}
		return r.Condition, nil
	}
	if len(r.Filters) > 32 {
		return nil, errors.New("too many filter rules")
	}
	return AndFilters(r.Filters), nil
}

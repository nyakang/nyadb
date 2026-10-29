package mysql

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// MySQLDataFilterRule is a bounded, AND-connected server-side condition.
type MySQLDataFilterRule struct {
	Column string
	Mode   string // equal, not_equal, contains, not_contains, like, not_like, less, greater, is_null, is_not_null
	Value  string
}

// MySQLDataSortRule is a server-side priority sort rule.
type MySQLDataSortRule struct {
	Column     string
	Descending bool
}

const (
	MySQLDataMaxFilterRules = 6
	MySQLDataMaxSortRules   = 4
)

func mysqlDataEffectiveRules(o MySQLDataOptions) ([]MySQLDataFilterRule, []MySQLDataSortRule, error) {
	if len(o.Filters) > MySQLDataMaxFilterRules || len(o.Sorts) > MySQLDataMaxSortRules ||
		(len(o.Filters) > 0 && o.FilterMode != "") || (len(o.Sorts) > 0 && o.SortColumn != "") {
		return nil, nil, ErrMySQLDataPage
	}
	filters := append([]MySQLDataFilterRule(nil), o.Filters...)
	if o.FilterMode != "" {
		filters = append(filters, MySQLDataFilterRule{Column: o.FilterColumn, Mode: o.FilterMode, Value: o.FilterValue})
	}
	sorts := append([]MySQLDataSortRule(nil), o.Sorts...)
	if o.SortColumn != "" {
		sorts = append(sorts, MySQLDataSortRule{Column: o.SortColumn, Descending: o.SortDescending})
	}
	for _, rule := range filters {
		if rule.Column == "" || len(rule.Value) > 4096 {
			return nil, nil, ErrMySQLDataPage
		}
		switch rule.Mode {
		case "equal", "not_equal", "contains", "not_contains", "like", "not_like", "less", "greater", "is_null", "is_not_null":
		default:
			return nil, nil, ErrMySQLDataPage
		}
	}
	used := make(map[string]bool, len(sorts))
	for _, rule := range sorts {
		if rule.Column == "" || used[rule.Column] {
			return nil, nil, ErrMySQLDataPage
		}
		used[rule.Column] = true
	}
	return filters, sorts, nil
}

func mysqlDataRuleSignature(filters []MySQLDataFilterRule, sorts []MySQLDataSortRule) string {
	canonical, _ := json.Marshal(struct {
		Filters []MySQLDataFilterRule
		Sorts   []MySQLDataSortRule
	}{filters, sorts})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

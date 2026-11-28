package schemaddl

import (
	"reflect"
	"strings"
)

// CREATE requires the captured absent target. The adapter signs and rechecks
// its complete native enumeration; this planner never guesses from SQL errors.
func PlanManticoreRTCreate(original Snapshot, desired Table) (Plan, error) {
	if original.Engine != "manticoresearch" || original.Exists || original.DDL != "" || original.Postgres != nil || original.OpenGauss != nil || original.SQLite != nil || original.Databend != nil || !reflect.DeepEqual(original.Table, Table{Name: original.Table.Name}) {
		return Plan{}, ErrUnsupported
	}
	if desired.Name != original.Table.Name || ManticoreIdentifier(desired.Name) != nil || desired.Name != strings.ToLower(desired.Name) || desired.Options != "" || len(desired.Definitions) != 0 || desired.Postgres != nil || desired.OpenGauss != nil || len(desired.Clauses) == 0 || len(desired.Clauses) > 512 {
		return Plan{}, ErrUnsupported
	}
	ids, names := map[string]bool{}, map[string]bool{}
	columns := make([]string, 0, len(desired.Clauses))
	for i, c := range desired.Clauses {
		if c.ID == "" || ids[c.ID] || names[c.Name] || c.Kind != "column" || c.Postgres != nil || c.OpenGauss != nil || ManticoreIdentifier(c.Name) != nil || c.Name != strings.ToLower(c.Name) {
			return Plan{}, ErrUnsupported
		}
		ids[c.ID], names[c.Name] = true, true
		body, err := ManticoreColumnDefinition(c.SQL)
		if err != nil || (i == 0 && (c.Name != "id" || body != "bigint")) || (i > 0 && c.Name == "id") {
			return Plan{}, ErrUnsupported
		}
		columns = append(columns, manticoreQuote(c.Name)+" "+body)
	}
	return Plan{Mode: "native_create", SQL: []string{"CREATE TABLE " + manticoreQuote(desired.Name) + " (" + strings.Join(columns, ", ") + ")"}, ScopeHint: "创建原目标名称的 RT 表，id 固定为原生 bigint。提交前从完整表列表检查名称未占用，不锁定并发创建。不能回滚；成功回执后核验完整结构，未知或未核验回执禁止重放。"}, nil
}

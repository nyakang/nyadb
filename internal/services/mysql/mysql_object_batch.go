package mysql

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/sqltext"
)

type ObjectBatchItem struct {
	Ref                 ObjectIdentity
	Action, Destination string
}
type ObjectBatchResult struct {
	Item    ObjectBatchItem
	Outcome ObjectOutcome
}

// CloneObjectSQL changes only the definition's public object identity. Its
// complete body, qualifiers, security options and definer are retained.
func CloneObjectSQL(source ObjectDefinition, destination string) (string, error) {
	if _, e := quotedMySQLIdentifier(destination); e != nil {
		return "", e
	}
	kind, e := objectKind(source.Kind)
	if e != nil || kind == "DATABASE" {
		return "", errors.New("database copies require a backup workflow")
	}
	ts, e := sqltext.Tokens(source.SQL)
	if e != nil {
		return "", e
	}
	for i, t := range ts {
		if sqltext.Word(t, kind) && i+1 < len(ts) {
			start, end := ts[i+1].Start, ts[i+1].End
			if i+3 < len(ts) && ts[i+2].Text == "." {
				end = ts[i+3].End
			}
			db, _ := quotedMySQLIdentifier(source.Database)
			name, _ := quotedMySQLIdentifier(destination)
			raw := source.SQL[:start] + db + "." + name + source.SQL[end:]
			ref := source.ObjectIdentity
			ref.Name = destination
			return raw, ValidateObjectSQL(ref, "create", raw)
		}
	}
	return "", errors.New("complete definition header is unavailable")
}

// ApplyObjectBatch stops after the first failed or uncertain item. Every item
// retains its original identity and acknowledgement; subsequent items remain
// explicitly not_executed and no successful SQL is automatically replayed.
func (m *MySQLSessionManager) ApplyObjectBatch(ctx context.Context, id int64, generation uint64, items []ObjectBatchItem) []ObjectBatchResult {
	out := make([]ObjectBatchResult, len(items))
	for i, item := range items {
		out[i] = ObjectBatchResult{Item: item, Outcome: ObjectOutcome{DDLOutcome: DDLOutcome{State: "not_executed"}}}
	}
	for i, item := range items {
		if ctx.Err() != nil || m.SessionGeneration(id) != generation {
			out[i].Outcome.Error = errors.Join(ctx.Err(), ErrMySQLNotConnected)
			break
		}
		var err error
		if item.Ref.Kind == "表" || item.Ref.Kind == "table" {
			action := MySQLTableDrop
			if item.Action == "copy" {
				action = MySQLTableCopyStructure
			} else if item.Action != "drop" {
				err = errors.New("unsupported table batch action")
			}
			if err == nil {
				err = m.ChangeTable(ctx, id, MySQLTableChange{RuntimeGeneration: generation, Action: action, Database: item.Ref.Database, Table: item.Ref.Name, NewName: item.Destination})
			}
			if err != nil {
				out[i].Outcome.State = "uncertain"
				out[i].Outcome.Error = err
				break
			}
			out[i].Outcome.State = "executed"
			out[i].Outcome.Verified = true
			continue
		}
		original, err := m.ObjectDefinition(ctx, id, item.Ref)
		if err != nil {
			out[i].Outcome.Error = err
			break
		}
		var plan ObjectPlan
		if item.Action == "copy" {
			raw, e := CloneObjectSQL(original, item.Destination)
			err = e
			if err == nil {
				original.Name = item.Destination
				original.SQL = ""
				original.Fingerprint = ""
				plan, err = BuildObjectPlan(original, "create", raw)
			}
		} else if item.Action == "drop" {
			plan, err = BuildObjectPlan(original, "drop", "")
		} else {
			err = errors.New("unsupported object batch action")
		}
		if err != nil {
			out[i].Outcome.Error = err
			break
		}
		out[i].Outcome = m.ApplyObject(ctx, id, plan)
		if out[i].Outcome.Error != nil || !out[i].Outcome.Verified {
			break
		}
	}
	return out
}

// SetObjectDefinitionName changes a create form's target without discarding
// its edited body or table-independent object options.
func SetObjectDefinitionName(source ObjectDefinition, destination string) (string, error) {
	kind, e := objectKind(source.Kind)
	if e != nil {
		return "", e
	}
	if kind != "DATABASE" {
		return CloneObjectSQL(source, destination)
	}
	if _, e = quotedMySQLIdentifier(destination); e != nil {
		return "", e
	}
	ts, e := sqltext.Tokens(source.SQL)
	if e != nil {
		return "", e
	}
	for i, t := range ts {
		if sqltext.Word(t, "DATABASE") && i+1 < len(ts) {
			q, _ := quotedMySQLIdentifier(destination)
			raw := source.SQL[:ts[i+1].Start] + q + source.SQL[ts[i+1].End:]
			ref := source.ObjectIdentity
			ref.Database = destination
			return raw, ValidateObjectSQL(ref, "create", raw)
		}
	}
	return "", errors.New("complete database definition header is unavailable")
}

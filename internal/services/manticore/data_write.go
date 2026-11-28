package manticore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataPlanMaxBytes = 32 << 20
const writeScope = "Independent native statements, without batch transaction or whole-row concurrency isolation. Existing rows are checked before each statement; scalar attributes participate in its predicate. Stored text, JSON and sets are preflight observations only."

type dataStatement struct {
	SQL   string
	token string
	grant *dataGrant
}
type dataWritePlan struct {
	owner            *dataLease
	snapshot         contract.DataMutationSnapshot
	statements       []dataStatement
	metadata         string
	closed, consumed bool
}

func (p *dataWritePlan) Snapshot() contract.DataMutationSnapshot {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	return contract.CloneDataMutationSnapshot(p.snapshot)
}
func (p *dataWritePlan) releaseLocked() {
	p.closed = true
	p.snapshot = contract.DataMutationSnapshot{}
	p.statements = nil
	if p.owner.plan == p && !p.consumed {
		p.owner.plan = nil
	}
}
func (p *dataWritePlan) Close() error {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	p.releaseLocked()
	return nil
}
func (d *dataLease) writeAdmission() error {
	if d.closed {
		return contract.ErrRevoked
	}
	if d.writeBlocked {
		return contract.ErrDataCommitUncertain
	}
	return nil
}
func (d *dataLease) prepareStatement(ctx context.Context, m *contract.TableStructure, e contract.DataEdit) (dataStatement, error) {
	s := dataStatement{}
	if e.GraphEndpoints != nil || !slices.Contains(writeKinds(m), e.Kind) {
		return s, contract.ErrUnsupported
	}
	if e.Kind == "insert" && e.Original != nil || e.Kind != "insert" && e.Original == nil || e.Kind == "delete" && len(e.Values) != 0 || e.Kind != "delete" && len(e.Values) == 0 {
		return s, connections.ErrInvalid
	}
	var names, values, assignments []string
	size := 0
	permissions := writePermissions(m)
	for _, c := range m.Columns {
		v, ok := e.Values[c.Name]
		if !ok {
			continue
		}
		if !slices.Contains(permissions[c.Name], e.Kind) && !(e.Kind == "replace" && c.Name == "id") {
			return s, contract.ErrUnsupported
		}
		size += len(v.Text) + len(v.Bytes)
		if size > contract.DataEditMaxBytes {
			return s, contract.ErrDataEditLimit
		}
		literal, err := editValueLiteral(m, e.Kind, c, v)
		if err != nil {
			return s, err
		}
		names = append(names, quote(c.Name))
		values = append(values, literal)
		assignments = append(assignments, quote(c.Name)+"="+literal)
	}
	if len(names) != len(e.Values) {
		return s, connections.ErrInvalid
	}
	if e.Kind == "replace" && len(e.Values) != len(m.Columns) {
		return s, connections.ErrInvalid
	}
	var predicates []string
	if e.Original != nil {
		token, g, err := d.grant(e.Original.Ref)
		if err != nil {
			return s, err
		}
		if err = validateSnapshot(e.Original, g); err != nil {
			return s, err
		}
		cells, err := d.readOriginal(ctx, m, g)
		if err != nil {
			return s, err
		}
		if e.Kind == "replace" {
			if e.Original.Mode != "values" {
				return s, connections.ErrInvalid
			}
			total := 0
			for _, cell := range e.Original.Original {
				if cell.Truncated {
					return s, connections.ErrInvalid
				}
				total += len(cell.Text) + len(cell.Bytes)
			}
			if total > contract.DataEditMaxBytes {
				return s, contract.ErrDataEditLimit
			}
			id, err := editValueLiteral(m, "replace", contract.StructureColumn{Name: "id", DataType: "bigint"}, e.Values["id"])
			originalID, originalErr := replacementIDLiteral(g.id)
			if err != nil || originalErr != nil || id != originalID {
				return s, connections.ErrInvalid
			}
		}
		if e.Kind == "update" {
			total := 0
			for i, c := range cells {
				total += len(c.Text) + len(c.Bytes)
				if next, ok := e.Values[m.Columns[i].Name]; ok {
					total += len(next.Text) + len(next.Bytes) - len(c.Text) - len(c.Bytes)
				}
			}
			if total > contract.DataEditMaxBytes {
				return s, contract.ErrDataEditLimit
			}
		}
		s.token, s.grant = token, g
		predicates = append(predicates, "id="+g.id)
		// Percolate accepts only the id predicate; its original record is checked
		// beforehand and does not acquire atomic compare-and-delete semantics.
		// These RT/plain attribute predicates narrow the original id. Full-row
		// comparison is a separate preflight observation, not an atomic CAS claim.
		for i, c := range m.Columns {
			if c.Name == "id" || m.Engine == "percolate" {
				continue
			}
			switch c.DataType {
			case "bigint", "uint", "integer", "timestamp", "bool", "string":
				literal, err := nativeLiteral(c, cells[i].Text)
				if err != nil {
					return s, err
				}
				predicates = append(predicates, quote(c.Name)+"="+literal)
			}
		}
	}
	name := quote(d.object.Catalog) + "." + quote(d.object.Name)
	switch e.Kind {
	case "insert":
		s.SQL = "INSERT INTO " + name + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
	case "replace":
		s.SQL = "REPLACE INTO " + name + " (" + strings.Join(names, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
	case "update":
		s.SQL = "UPDATE " + name + " SET " + strings.Join(assignments, ",") + " WHERE " + strings.Join(predicates, " AND ")
	case "delete":
		s.SQL = "DELETE FROM " + name + " WHERE " + strings.Join(predicates, " AND ")
	}
	if len(s.SQL) > 8<<20 {
		return dataStatement{}, contract.ErrDataEditLimit
	}
	return s, nil
}
func (d *dataLease) previewEdits(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if ctx == nil || len(edits) < 1 || len(edits) > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	if err := d.identity(id, db, table); err != nil {
		return nil, err
	}
	d.mu.Lock()
	if err := d.writeAdmission(); err != nil {
		d.mu.Unlock()
		return nil, err
	}
	if d.planning || d.plan != nil {
		d.mu.Unlock()
		return nil, contract.ErrSQLSessionLimit
	}
	d.planning = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.planning = false; d.mu.Unlock() }()
	snapshot := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Object: d.object, Edits: edits})
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > dataPlanMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	size := len(raw)
	child, done, err := d.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	m, err := d.table(child)
	if err != nil {
		return nil, err
	}
	scope := writeScope
	if m.Engine == "percolate" {
		scope = "Independent native statements without a batch transaction. Percolate deletion uses only the native id predicate. The complete stored record is checked before each statement, without atomic compare-and-delete; a change after that check may still be deleted. Omitted insert fields use native defaults. UPDATE is unsupported and is never replaced with REPLACE."
		snapshot.Presentation = &contract.DataMutationPresentation{Title: "确认 Manticore 存储查询写入", Hint: "各语句独立执行，不提供批次事务。删除仅以原生 id 为条件；完整存储记录仅在提交前检查，不保证原子比较删除，检查后发生的更改仍可能被删除。新增时省略的字段使用原生默认值。未知或不完整回执保留草稿并禁止重放。", PreviewLabel: "Manticore 存储查询写入完整预览", HistoryLabel: "已确认的 Manticore 存储查询写入", CreateLabel: "新增存储查询", DeleteLabel: "删除存储查询"}
	}
	if m.Engine == "percolate" && slices.ContainsFunc(snapshot.Edits, func(e contract.DataEdit) bool { return e.Kind == "replace" }) {
		scope = "Independent native statements without a batch transaction. Explicit REPLACE sends every stored field with the original id. The complete original record is checked before dispatch, without atomic compare-and-replace: a later change may be overwritten and a deleted record may be recreated. DELETE remains id-only without atomic compare-and-delete. Omitted INSERT fields use native defaults. UPDATE is unsupported."
		snapshot.Presentation = &contract.DataMutationPresentation{Title: "确认 Manticore 存储查询替换", Hint: "替换会提交原 id 和全部存储字段。完整原记录仅在提交前检查，不保证原子比较替换；检查后的更改可能被覆盖，已删除的记录可能被重新创建。各语句独立执行，无批次事务；删除仅以 id 为条件，新增省略字段使用原生默认值。未知或不完整回执保留草稿并禁止重放。", PreviewLabel: "Manticore 存储查询替换完整预览", HistoryLabel: "已确认的 Manticore 存储查询替换", CreateLabel: "新增存储查询", DeleteLabel: "删除存储查询", ReplaceLabel: "替换整条存储查询"}
	}
	if m.Engine == "rt" && slices.ContainsFunc(snapshot.Edits, func(e contract.DataEdit) bool { return e.Kind == "replace" }) {
		scope = "Independent native statements without a batch transaction. Explicit RT REPLACE sends every stored field and attribute with the original id and rebuilds its full-text index. Complete original data is checked before dispatch, without atomic compare-and-replace; later changes may be overwritten and deleted documents recreated. Native JSON null is encoded as quoted JSON null, distinct from an empty object. Existing UPDATE and DELETE retain their native attribute predicates. Omitted INSERT fields use native defaults."
		snapshot.Presentation = &contract.DataMutationPresentation{Title: "确认 Manticore RT 整条替换", Hint: "替换会提交原 id、全部存储字段和属性，并重建全文索引。完整原记录仅在提交前检查，不保证原子比较替换；检查后的更改可能被覆盖，已删除的记录可能被重新创建。原生 JSON 空值使用 JSON null 字面量保留，不会变成空对象。各语句独立执行，无批次事务；未知或不完整回执保留草稿并禁止重放。", PreviewLabel: "Manticore RT 整条替换完整预览", HistoryLabel: "已确认的 Manticore RT 整条替换", CreateLabel: "新增记录", UpdateLabel: "更新记录", DeleteLabel: "删除记录", ReplaceLabel: "替换整条 RT 记录"}
	}

	var statements []dataStatement
	seen := map[string]bool{}
	for _, edit := range snapshot.Edits {
		s, err := d.prepareStatement(child, m, edit)
		if err != nil {
			return nil, err
		}
		entity := ""
		if edit.Kind == "insert" {
			if id, ok := edit.Values["id"]; ok {
				literal, e := writeLiteral(contract.StructureColumn{Name: "id", DataType: "bigint"}, id)
				if e != nil {
					return nil, e
				}
				if literal != "0" {
					entity = literal
				}
			}
		}
		if s.grant != nil {
			entity = s.grant.id
			if seen[entity] {
				return nil, connections.ErrInvalid
			}
			seen[entity] = true
		}
		size += len(s.SQL)
		if size > dataPlanMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		statements = append(statements, s)
		snapshot.Items = append(snapshot.Items, contract.DataMutationItem{Kind: edit.Kind, EntityID: entity, Destination: d.object})
	}
	if err = d.primary.Preconnect(child); err != nil {
		return nil, err
	}
	sig := metadataSignature(m)
	raw, err = json.Marshal(struct {
		Object          contract.ObjectRef
		Metadata, Scope string
		Statements      []dataStatement
	}{d.object, sig, scope, statements})
	if err != nil || len(raw)+size > dataPlanMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	snapshot.NativeJSON = string(raw)
	p := &dataWritePlan{owner: d, snapshot: snapshot, statements: statements, metadata: sig}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err = d.writeAdmission(); err != nil {
		return nil, err
	}
	if err = child.Err(); err != nil {
		return nil, err
	}
	d.plan = p
	return p, nil
}
func (d *dataLease) submitEdits(ctx context.Context, id int64, db, table string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := d.identity(id, db, table); err != nil {
		return nil, err
	}
	p, ok := supplied.(*dataWritePlan)
	if !ok || p == nil || p.owner != d {
		return nil, connections.ErrInvalid
	}
	d.mu.Lock()
	if err := d.writeAdmission(); err != nil {
		d.mu.Unlock()
		return nil, err
	}
	if d.plan != p || p.closed || p.consumed {
		d.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	p.consumed = true
	snapshot := contract.CloneDataMutationSnapshot(p.snapshot)
	statements := slices.Clone(p.statements)
	signature := p.metadata
	d.mu.Unlock()
	result := &contract.DataEditResult{Sequential: &contract.DataSequentialWriteReceipt{Object: d.object}}
	for i, e := range snapshot.Edits {
		result.Sequential.Statements = append(result.Sequential.Statements, contract.DataWriteStatementReceipt{EditIndex: i, Kind: e.Kind, Destination: d.object, State: contract.DataWriteNotSent})
	}
	conflict := false
	child, done, err := d.request(ctx)
	if err == nil {
		defer done()
		for i, s := range statements {
			if err = child.Err(); err != nil {
				break
			}
			var m *contract.TableStructure
			m, err = d.table(child)
			if err != nil {
				break
			}
			if metadataSignature(m) != signature {
				err = contract.ErrDataConflict
				break
			}
			if s.grant != nil {
				if err = d.currentGrant(s.token, s.grant); err != nil {
					break
				}
				if _, err = d.readOriginal(child, m, s.grant); err != nil {
					break
				}
			}
			var original nativeWriteReceipt
			original, err = d.primary.write(child, s.SQL)
			step := &result.Sequential.Statements[i]
			if original.Dispatched {
				raw, _ := json.Marshal(original)
				step.NativeJSON = string(raw)
				step.State = contract.DataWriteUnknown
				if original.Acknowledged {
					if original.ErrorCode != 0 {
						step.State = contract.DataWriteFailed
					} else {
						step.State = contract.DataWriteAcknowledged
						step.AffectedRows = original.AffectedRows
					}
				}
			}
			if step.State != contract.DataWriteAcknowledged {
				break
			}
			result.Applied++
			result.Outcomes = append(result.Outcomes, contract.DataEditOutcome{Kind: snapshot.Edits[i].Kind, Original: snapshot.Edits[i].Original, GeneratedID: original.LastInsertID})
			if step.AffectedRows != 1 {
				conflict = true
				err = errors.Join(err, contract.ErrDataConflict)
				break
			}
			if err != nil {
				break
			}
		}
	}
	result.Incomplete = conflict || err != nil || result.Applied != len(statements)
	result.RefreshRequired = result.Applied > 0
	for _, s := range result.Sequential.Statements {
		result.CommitUncertain = result.CommitUncertain || s.State == contract.DataWriteUnknown
	}
	if result.CommitUncertain {
		err = errors.Join(err, contract.ErrDataCommitUncertain)
	}
	if result.Incomplete && err == nil {
		err = contract.ErrDataConflict
	}
	raw, _ := json.Marshal(struct {
		Request json.RawMessage
		Receipt *contract.DataSequentialWriteReceipt
	}{json.RawMessage(snapshot.NativeJSON), result.Sequential})
	result.NativeJSON = string(raw)
	d.mu.Lock()
	if result.Incomplete {
		d.writeBlocked = true
	}
	if d.plan == p {
		d.plan = nil
	}
	p.releaseLocked()
	d.mu.Unlock()
	return result, err
}

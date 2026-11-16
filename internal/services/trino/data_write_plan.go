package trino

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataPlanBytes = 32 << 20

type dataWritePlan struct {
	owner            *dataLease
	snapshot         contract.DataMutationSnapshot
	statements       []string
	columns          []contract.StructureColumn
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
	p.columns = nil
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
func (l *dataLease) writeAdmission() error {
	if l.closed {
		return contract.ErrRevoked
	}
	if !strings.EqualFold(l.object.Kind, "TABLE") {
		return contract.ErrUnsupported
	}
	if l.writeBlocked {
		return contract.ErrDataCommitUncertain
	}
	return nil
}
func (l *dataLease) previewEdits(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.mu.Lock()
	if err = l.writeAdmission(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if l.planning || l.plan != nil {
		l.mu.Unlock()
		return nil, contract.ErrSQLSessionLimit
	}
	l.planning = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.planning = false; l.mu.Unlock() }()
	snapshot := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Object: l.object, Edits: edits})
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > dataPlanBytes {
		return nil, contract.ErrDataEditLimit
	}
	snapshotBytes := len(raw)
	m, err := l.session.TableMetadata(ctx, l.object)
	if err != nil {
		return nil, err
	}
	statements := make([]string, len(snapshot.Edits))
	for i, e := range snapshot.Edits {
		if e.Kind != "insert" || e.Original != nil || e.GraphEndpoints != nil {
			return nil, contract.ErrUnsupported
		}
		q, err := insertStatement(m, e.Values)
		if err != nil {
			return nil, err
		}
		statements[i] = q
		snapshot.Items = append(snapshot.Items, contract.DataMutationItem{Kind: "insert", EntityID: "INSERT #" + strconv.Itoa(i+1), Destination: l.object})
	}
	raw, err = json.Marshal(struct {
		Object     contract.ObjectRef
		Columns    []contract.StructureColumn
		Statements []string
		Scope      string
	}{l.object, m.Columns, statements, "Independent native INSERT statements; original transport; no batch transaction, primary-key update, or replay"})
	if err != nil || len(raw)+snapshotBytes > dataPlanBytes {
		return nil, contract.ErrDataEditLimit
	}
	snapshot.NativeJSON = string(raw)
	p := &dataWritePlan{owner: l, snapshot: snapshot, statements: statements, columns: slices.Clone(m.Columns)}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.writeAdmission(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	l.plan = p
	return p, nil
}
func insertStatement(m *contract.TableStructure, values map[string]contract.DataValue) (string, error) {
	if m == nil || !strings.EqualFold(m.Kind, "TABLE") {
		return "", contract.ErrUnsupported
	}
	known := map[string]bool{}
	for _, c := range m.Columns {
		known[c.Name] = true
	}
	size := 0
	for name, v := range values {
		if !known[name] {
			return "", connections.ErrInvalid
		}
		size += len(v.Text) + len(v.Bytes)
	}
	if size > contract.DataEditMaxBytes {
		return "", contract.ErrDataEditLimit
	}
	var columns, literals []string
	for _, c := range m.Columns {
		v, ok := values[c.Name]
		if !ok {
			continue
		}
		if c.Generated || contract.DataColumnGenerated(c) {
			return "", contract.ErrUnsupported
		}
		t, err := parseInsertType(c.Type)
		if err != nil {
			return "", err
		}
		s, err := insertLiteral(t, v)
		if err != nil {
			return "", err
		}
		columns = append(columns, quoteName(c.Name))
		literals = append(literals, s)
	}
	if len(columns) == 0 {
		return "", contract.ErrUnsupported
	}
	q := "INSERT INTO " + qualified(m.Object) + "(" + strings.Join(columns, ",") + ") VALUES(" + strings.Join(literals, ",") + ")"
	if len(q) > statementBytes {
		return "", contract.ErrDataEditLimit
	}
	return q, nil
}
func (l *dataLease) submitEdits(ctx context.Context, id int64, db, table string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	p, ok := supplied.(*dataWritePlan)
	if !ok || p == nil || p.owner != l {
		return nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if err = l.writeAdmission(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if l.plan != p || p.closed || p.consumed {
		l.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	p.consumed = true
	snapshot := contract.CloneDataMutationSnapshot(p.snapshot)
	statements := slices.Clone(p.statements)
	columns := slices.Clone(p.columns)
	l.mu.Unlock()
	result := &contract.DataEditResult{Sequential: &contract.DataSequentialWriteReceipt{Object: l.object}}
	for i := range statements {
		result.Sequential.Statements = append(result.Sequential.Statements, contract.DataWriteStatementReceipt{EditIndex: i, Kind: "insert", Destination: l.object, State: contract.DataWriteNotSent})
	}
	err = l.executePlan(ctx, columns, statements, result)
	result.Incomplete = result.Applied != len(statements)
	result.RefreshRequired = result.Applied > 0
	for _, r := range result.Sequential.Statements {
		result.CommitUncertain = result.CommitUncertain || r.State == contract.DataWriteUnknown
	}
	if result.CommitUncertain {
		err = errors.Join(contract.ErrDataCommitUncertain, err)
	}
	if result.Incomplete && err == nil {
		err = contract.ErrDataConflict
	}
	raw, _ := json.Marshal(struct {
		Request json.RawMessage
		Receipt *contract.DataSequentialWriteReceipt
	}{json.RawMessage(snapshot.NativeJSON), result.Sequential})
	result.NativeJSON = string(raw)
	l.mu.Lock()
	if result.Incomplete {
		l.writeBlocked = true
	}
	if l.plan == p {
		l.plan = nil
	}
	p.releaseLocked()
	l.mu.Unlock()
	return result, err
}
func (l *dataLease) executePlan(ctx context.Context, columns []contract.StructureColumn, statements []string, result *contract.DataEditResult) error {
	ctx, done, err := l.session.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	r, err := l.session.reader(ctx, false)
	if err != nil {
		return err
	}
	defer r.Close()
	executor, ok := r.(insertExecutor)
	if !ok {
		return contract.ErrUnsupported
	}
	for i, q := range statements {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Fresh native metadata guards every independent statement's schema.
		m, err := tableMetadata(ctx, r, l.object)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(columns, m.Columns) {
			return contract.ErrDataConflict
		}
		attempt, err := executor.insert(ctx, q)
		s := &result.Sequential.Statements[i]
		s.State, s.AffectedRows, s.NativeJSON = attempt.State, attempt.Affected, attempt.NativeJSON
		if attempt.State == contract.DataWriteAcknowledged {
			result.Applied++
			result.Outcomes = append(result.Outcomes, contract.DataEditOutcome{Kind: "insert"})
			continue
		}
		if err == nil {
			err = ErrProtocol
		}
		return err
	}
	return nil
}

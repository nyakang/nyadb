package prestosql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

const dataPlanMaxBytes = 32 << 20

type dataWritePlan struct {
	owner            *dataLease
	snapshot         contract.DataMutationSnapshot
	statements       []string
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
func (l *dataLease) writeAdmission() error {
	if l.closed {
		return contract.ErrRevoked
	}
	if l.writeBlocked {
		return contract.ErrDataCommitUncertain
	}
	if l.object.Kind != "table" {
		return contract.ErrUnsupported
	}
	return nil
}
func dataMetadataSignature(m *contract.TableStructure) string {
	raw, _ := json.Marshal(struct {
		Object  contract.ObjectRef
		Columns []contract.StructureColumn
		Kind    string
	}{m.Object, m.Columns, m.Kind})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func insertStatement(m *contract.TableStructure, edit contract.DataEdit) (string, error) {
	if edit.Kind != "insert" || edit.Original != nil || edit.GraphEndpoints != nil {
		return "", contract.ErrUnsupported
	}
	if len(edit.Values) == 0 || len(edit.Values) > len(m.Columns) {
		return "", connections.ErrInvalid
	}
	var columns, values []string
	total := 0
	for _, c := range m.Columns {
		value, exists := edit.Values[c.Name]
		if !exists {
			continue
		}
		if c.Generated || contract.DataColumnGenerated(c) {
			return "", contract.ErrUnsupported
		}
		total += len(value.Text) + len(value.Bytes)
		if total > contract.DataEditMaxBytes {
			return "", contract.ErrDataEditLimit
		}
		typ, err := parseInsertType(c.Type)
		if err != nil {
			return "", err
		}
		if !c.Nullable && (value.Null || value.Kind == contract.Null) {
			return "", connections.ErrInvalid
		}
		literal, err := insertLiteral(typ, value)
		if err != nil {
			return "", err
		}
		columns = append(columns, quoteName(c.Name))
		values = append(values, literal)
	}
	if len(columns) != len(edit.Values) {
		return "", connections.ErrInvalid
	}
	source := "INSERT INTO " + qualified(m.Object) + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
	if len(source) > 8<<20 {
		return "", contract.ErrDataEditLimit
	}
	return source, nil
}
func (l *dataLease) previewEdits(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if ctx == nil || len(edits) < 1 || len(edits) > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if err := l.writeAdmission(); err != nil {
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
	if err != nil || len(raw) > dataPlanMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	used := len(raw)
	child, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	m, err := l.session.TableMetadata(child, l.object)
	if err != nil {
		return nil, err
	}
	var statements []string
	for _, edit := range snapshot.Edits {
		source, err := insertStatement(m, edit)
		if err != nil {
			return nil, err
		}
		used += len(source)
		if used > dataPlanMaxBytes {
			return nil, contract.ErrDataEditLimit
		}
		statements = append(statements, source)
		snapshot.Items = append(snapshot.Items, contract.DataMutationItem{Kind: "insert", Destination: l.object})
	}
	if err = l.primary.Preconnect(child); err != nil {
		return nil, err
	}
	signature := dataMetadataSignature(m)
	raw, err = json.Marshal(struct {
		Object        contract.ObjectRef
		Metadata      string
		Statements    []string
		Transactional bool
	}{l.object, signature, statements, false})
	if err != nil || len(raw) > dataPlanMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	snapshot.NativeJSON = string(raw)
	plan := &dataWritePlan{owner: l, snapshot: snapshot, statements: statements, metadata: signature}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.writeAdmission(); err != nil {
		return nil, err
	}
	if err = child.Err(); err != nil {
		return nil, err
	}
	l.plan = plan
	return plan, nil
}

// Only validated query result envelopes enter ordinary receipt JSON. An
// unexpected outer native error may contain connection secrets; its complete
// bytes remain in the owning document/private observer, with a hash here.
func dataReceiptJSON(receipt jdbcplugin.Receipt, result queryResult) string {
	original := receipt.OriginalResponse()
	safe := ""
	if receipt.ExecutorAcknowledged && (result.completed || receipt.Diagnostic != nil) {
		fields, err := resultFields(original)
		var id uint64
		if err == nil && len(fields) == 2 && json.Unmarshal(fields["id"], &id) == nil && id > 0 && bytes.Equal(fields["result"], receipt.Result) {
			safe = string(original)
		}
	}
	digest := sha256.Sum256(original)
	raw, _ := json.Marshal(struct {
		Receipt                   jdbcplugin.Receipt
		OriginalResponse          string
		OriginalSHA256            string
		OriginalRetainedPrivately bool
	}{receipt, safe, hex.EncodeToString(digest[:]), len(original) > 0 && safe == ""})
	return string(raw)
}
func (l *dataLease) submitEdits(ctx context.Context, id int64, db, table string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	plan, ok := supplied.(*dataWritePlan)
	if !ok || plan == nil || plan.owner != l {
		return nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if err := l.writeAdmission(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if l.plan != plan || plan.closed || plan.consumed {
		l.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	plan.consumed = true
	snapshot := contract.CloneDataMutationSnapshot(plan.snapshot)
	statements := slices.Clone(plan.statements)
	signature := plan.metadata
	l.mu.Unlock()
	result := &contract.DataEditResult{Sequential: &contract.DataSequentialWriteReceipt{Object: l.object}}
	for i := range statements {
		result.Sequential.Statements = append(result.Sequential.Statements, contract.DataWriteStatementReceipt{EditIndex: i, Kind: "insert", Destination: l.object, State: contract.DataWriteNotSent})
	}
	conflict := false
	child, done, err := l.request(ctx)
	if err == nil {
		defer done()
		for i, source := range statements {
			if err = child.Err(); err != nil {
				break
			}
			var metadata *contract.TableStructure
			metadata, err = l.session.TableMetadata(child, l.object)
			if err != nil {
				break
			}
			if dataMetadataSignature(metadata) != signature {
				err = contract.ErrDataConflict
				break
			}
			value, receipt, queryErr := l.primary.executeNative(child, jdbcplugin.Query{SQL: source, MaxRows: 1, TimeoutSeconds: l.session.config.QueryTimeoutSeconds})
			step := &result.Sequential.Statements[i]
			if receipt.Dispatched {
				step.NativeJSON = dataReceiptJSON(receipt, value)
				step.State = contract.DataWriteUnknown
				if value.completed && !value.truncated && len(value.columns) == 0 && len(value.rows) == 0 {
					step.State = contract.DataWriteAcknowledged
					step.AffectedRows = value.affectedRows
				} else if receipt.ExecutorAcknowledged && receipt.Diagnostic != nil && receipt.Diagnostic.Category == "sql" && receipt.Diagnostic.HasVendorCode && !receipt.ProcessExitConfirmed {
					step.State = contract.DataWriteFailed
				}
			}
			if step.State != contract.DataWriteAcknowledged {
				if step.State == contract.DataWriteUnknown {
					captured := receipt.Clone()
					l.mu.Lock()
					l.unresolved = &captured
					l.mu.Unlock()
				}
				err = errors.Join(queryErr, contract.ErrDataConflict)
				break
			}
			result.Applied++
			result.Outcomes = append(result.Outcomes, contract.DataEditOutcome{Kind: "insert"})
			if step.AffectedRows != 1 {
				conflict = true
				err = errors.Join(queryErr, contract.ErrDataConflict)
				break
			}
			if queryErr != nil {
				err = queryErr
				break
			}
		}
	}
	result.Incomplete = conflict || result.Applied != len(statements)
	result.RefreshRequired = result.Applied > 0
	for _, step := range result.Sequential.Statements {
		result.CommitUncertain = result.CommitUncertain || step.State == contract.DataWriteUnknown
	}
	if result.CommitUncertain {
		err = errors.Join(contract.ErrDataCommitUncertain, err)
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
	if l.plan == plan {
		l.plan = nil
	}
	plan.releaseLocked()
	l.mu.Unlock()
	return result, err
}

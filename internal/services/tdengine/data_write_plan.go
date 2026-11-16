package tdengine

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type nativeDataStatement struct {
	Destination   contract.ObjectRef
	SQL, KeyWhere string
	Grant         dataRowGrant `json:"-"`
}

type dataWritePlan struct {
	owner            *dataLease
	snapshot         contract.DataMutationSnapshot
	statements       []nativeDataStatement
	metadata         string
	epoch            uint64
	closed, consumed bool
}

// A plan retains original snapshots, typed edits and encoded native requests.
// The complete editor limit applies to each row/value, not duplicated JSON.
const dataWritePlanMaxBytes = 32 << 20

func (p *dataWritePlan) Snapshot() contract.DataMutationSnapshot {
	p.owner.mu.Lock()
	defer p.owner.mu.Unlock()
	return contract.CloneDataMutationSnapshot(p.snapshot)
}
func (p *dataWritePlan) releaseLocked() {
	p.closed = true
	p.snapshot = contract.DataMutationSnapshot{}
	p.statements = nil
	if p.owner.writePlan == p && !p.consumed {
		p.owner.writePlan = nil
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
	if l.session.profile.TDengineOptions.ReadOnly {
		return contract.ErrUnsupported
	}
	if l.writeBlocked {
		return contract.ErrDataCommitUncertain
	}
	return nil
}

func (l *dataLease) previewEdits(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
		return nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if err := l.writeAdmission(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if l.planning || l.writePlan != nil {
		l.mu.Unlock()
		return nil, contract.ErrSQLSessionLimit
	}
	l.planning = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.planning = false; l.mu.Unlock() }()
	captured := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Object: l.object, Edits: edits})
	raw, err := json.Marshal(captured)
	if err != nil || len(raw) > dataWritePlanMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	snapshotBytes := len(raw)
	var plan *dataWritePlan
	err = l.withWire(ctx, func(child context.Context, w *wire) error {
		m, e := l.readMetadata(child, w)
		if e != nil {
			return e
		}
		statements := make([]nativeDataStatement, 0, len(captured.Edits))
		seen := map[string]bool{}
		for _, edit := range captured.Edits {
			statement, e := l.prepareDataStatement(m, edit)
			if e != nil {
				return e
			}
			identity := qualified(statement.Destination.Catalog, statement.Destination.Name) + " WHERE " + statement.KeyWhere
			if seen[identity] {
				return connections.ErrInvalid
			}
			seen[identity] = true
			if edit.Original != nil {
				if e = l.checkOriginalEdit(child, w, m, statement.Grant, edit.Original); e != nil {
					return e
				}
			}
			statements = append(statements, statement)
			captured.Items = append(captured.Items, contract.DataMutationItem{Kind: edit.Kind, EntityID: identity, Destination: statement.Destination})
		}
		raw, e = json.Marshal(struct {
			Object     contract.ObjectRef
			Precision  string
			Metadata   string
			Statements []nativeDataStatement
			Scope      string
		}{l.object, m.Precision, m.Signature, statements, "Independent native INSERT merge / DELETE; no transaction or CAS; omitted fields retained and explicit NULL supplied"})
		if e != nil || len(raw)+snapshotBytes > dataWritePlanMaxBytes {
			return contract.ErrDataEditLimit
		}
		captured.NativeJSON = string(raw)
		plan = &dataWritePlan{owner: l, snapshot: captured, statements: statements, metadata: m.Signature, epoch: m.Epoch}
		return nil
	})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.writeAdmission(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	l.writePlan = plan
	return plan, nil
}

func (l *dataLease) prepareDataStatement(m *dataMetadata, e contract.DataEdit) (nativeDataStatement, error) {
	s := nativeDataStatement{Destination: l.object}
	if e.GraphEndpoints != nil {
		return s, contract.ErrUnsupported
	}
	if e.Kind != "insert" && e.Kind != "update" && e.Kind != "delete" {
		return s, contract.ErrUnsupported
	}
	values := map[string]contract.DataValue{}
	editBytes := 0
	for k, v := range e.Values {
		values[k] = v
		editBytes += len(v.Text) + len(v.Bytes)
	}
	if editBytes > contract.DataEditMaxBytes {
		return s, contract.ErrDataEditLimit
	}
	if e.Kind == "update" {
		if e.Original == nil {
			return s, connections.ErrInvalid
		}
		if e.Original.Mode != "values" {
			return s, contract.ErrDataEditLimit
		}
		originalBytes := 0
		for _, c := range e.Original.Original {
			originalBytes += len(c.Text) + len(c.Bytes)
		}
		if originalBytes > contract.DataEditMaxBytes {
			return s, contract.ErrDataEditLimit
		}
		completeBytes := originalBytes
		for name, v := range values {
			i := slices.Index(e.Original.Columns, name)
			if i >= 0 && i < len(e.Original.Original) {
				original := e.Original.Original[i]
				completeBytes += len(v.Text) + len(v.Bytes) - len(original.Text) - len(original.Bytes)
			}
		}
		if completeBytes > contract.DataEditMaxBytes {
			return s, contract.ErrDataEditLimit
		}
	}
	if e.Kind == "insert" {
		if e.Original != nil {
			return s, connections.ErrInvalid
		}
		if l.object.Kind == "stable" {
			v, ok := values["tbname"]
			if !ok || v.Kind != contract.Text || v.Null || v.Default || v.Binary || len(v.Bytes) != 0 {
				return s, connections.ErrInvalid
			}
			s.Destination, ok = m.Children[v.Text]
			if !ok {
				return s, contract.ErrDataConflict
			}
			delete(values, "tbname")
		}
		s.Grant = dataRowGrant{Object: l.object, Destination: s.Destination, Precision: m.Precision, Metadata: m.Signature, Epoch: m.Epoch}
		for _, i := range m.Keys {
			c := m.Columns[i]
			if c.Name == "tbname" && l.object.Kind == "stable" {
				s.Grant.Keys = append(s.Grant.Keys, contract.Cell{Kind: contract.Text, Text: s.Destination.Name})
				s.Grant.KeyColumns = append(s.Grant.KeyColumns, c.Name)
				continue
			}
			v, ok := values[c.Name]
			if !ok || v.Null || v.Kind == contract.Null {
				return s, connections.ErrInvalid
			}
			if _, err := nativeDataLiteral(c, v, m.Precision); err != nil {
				return s, err
			}
			s.Grant.Keys = append(s.Grant.Keys, contract.Cell{Kind: v.Kind, Text: v.Text, Bytes: slices.Clone(v.Bytes)})
			s.Grant.KeyColumns = append(s.Grant.KeyColumns, c.Name)
		}
	} else {
		if e.Original == nil {
			return s, connections.ErrInvalid
		}
		g, err := l.grant(e.Original.Ref)
		if err != nil {
			return s, err
		}
		if err = validateDataGrant(g, m, e.Original.Ref); err != nil {
			return s, err
		}
		s.Grant, s.Destination = g, g.Destination
		if e.Kind == "update" && len(values) == 0 {
			return s, connections.ErrInvalid
		}
	}
	where, err := dataGrantWhere(s.Grant, m)
	if err != nil {
		return s, err
	}
	s.KeyWhere = where
	if e.Kind == "delete" {
		keys := len(m.Keys)
		if l.object.Kind == "stable" {
			keys--
		}
		if keys != 1 || len(values) != 0 {
			return s, contract.ErrUnsupported
		}
		s.SQL = "DELETE FROM " + qualified(s.Destination.Catalog, s.Destination.Name) + " WHERE " + where
		return s, nil
	}
	if e.Kind == "update" {
		for _, i := range m.Keys {
			c := m.Columns[i]
			if c.Name == "tbname" && l.object.Kind == "stable" {
				continue
			}
			key := s.Grant.Keys[slices.Index(s.Grant.KeyColumns, c.Name)]
			values[c.Name] = contract.DataValue{Kind: key.Kind, Text: key.Text, Bytes: slices.Clone(key.Bytes), Binary: key.Kind == contract.Binary}
		}
	}
	columns, literals := []string{}, []string{}
	for name := range e.Values {
		index := slices.IndexFunc(m.Columns, func(c contract.StructureColumn) bool { return c.Name == name })
		if index < 0 || !slices.Contains(m.Permissions[name], e.Kind) {
			return s, contract.ErrUnsupported
		}
	}
	for _, c := range m.Columns {
		v, ok := values[c.Name]
		if !ok {
			continue
		}
		encoded, err := nativeDataLiteral(c, v, m.Precision)
		if err != nil {
			return s, err
		}
		columns = append(columns, quote(c.Name))
		literals = append(literals, encoded)
	}
	if len(columns) == 0 {
		return s, connections.ErrInvalid
	}
	s.SQL = "INSERT INTO " + qualified(s.Destination.Catalog, s.Destination.Name) + "(" + strings.Join(columns, ",") + ") VALUES(" + strings.Join(literals, ",") + ")"
	return s, nil
}

func (l *dataLease) checkOriginalEdit(ctx context.Context, w *wire, m *dataMetadata, g dataRowGrant, s *contract.RowSnapshot) error {
	if s.Metadata != string(s.Ref.Values[len(s.Ref.Values)-1]) || !reflect.DeepEqual(s.Fingerprints, g.Fingerprints) {
		return contract.ErrDataConflict
	}
	columns := make([]string, len(m.Columns))
	for i, c := range m.Columns {
		columns[i] = c.Name
	}
	if !slices.Equal(s.Columns, columns) || s.Mode != "values" && s.Mode != "fingerprints" {
		return contract.ErrDataConflict
	}
	if s.Mode == "values" {
		if len(s.Original) != len(columns) {
			return contract.ErrDataConflict
		}
		for i, c := range s.Original {
			if c.Truncated || !reflect.DeepEqual(fingerprintCell(c), g.Fingerprints[i]) {
				return contract.ErrDataConflict
			}
		}
	} else if len(s.Original) != 0 {
		return contract.ErrDataConflict
	}
	cells, err := l.readOriginal(ctx, w, m, g, s.Ref, columns)
	if err != nil {
		return err
	}
	for i, c := range cells {
		if !reflect.DeepEqual(fingerprintCell(c), g.Fingerprints[i]) {
			return contract.ErrDataConflict
		}
	}
	return nil
}

func (l *dataLease) submitEdits(ctx context.Context, id int64, db, table string, supplied contract.DataMutationPlan) (*contract.DataEditResult, error) {
	if ctx == nil {
		return nil, connections.ErrInvalid
	}
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	p, ok := supplied.(*dataWritePlan)
	if !ok || p == nil || p.owner != l {
		return nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if err := l.writeAdmission(); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if l.writePlan != p || p.closed || p.consumed {
		l.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	p.consumed = true
	snapshot := contract.CloneDataMutationSnapshot(p.snapshot)
	statements := slices.Clone(p.statements)
	metadata, epoch := p.metadata, p.epoch
	l.mu.Unlock()
	result := &contract.DataEditResult{Sequential: &contract.DataSequentialWriteReceipt{Object: l.object}}
	for i, e := range snapshot.Edits {
		result.Sequential.Statements = append(result.Sequential.Statements, contract.DataWriteStatementReceipt{EditIndex: i, Kind: e.Kind, Destination: statements[i].Destination, State: contract.DataWriteNotSent})
	}
	err := l.withWire(ctx, func(child context.Context, w *wire) error {
		for i, s := range statements {
			if child.Err() != nil {
				return child.Err()
			}
			// Every statement is independent; a previous acknowledgement cannot
			// authorize a suffix after metadata/child membership changes.
			m, err := l.readMetadata(child, w)
			if err != nil {
				return err
			}
			if m.Signature != metadata || m.Epoch != epoch {
				return contract.ErrDataConflict
			}
			if snapshot.Edits[i].Original != nil {
				if err = l.checkOriginalEdit(child, w, m, s.Grant, snapshot.Edits[i].Original); err != nil {
					return err
				}
			}
			dispatched := false
			_, raw, queryErr := w.queryAttempt(child, s.SQL, nil, nil, func() { dispatched = true })
			r := &result.Sequential.Statements[i]
			if !dispatched {
				return queryErr
			}
			r.State = contract.DataWriteUnknown
			r.NativeJSON = string(raw)
			var response nativeResponse
			if nativeDataResponse(raw, w.seq, &response) {
				if response.Code != 0 {
					r.State = contract.DataWriteFailed
					return &serverError{response.Code, response.Message}
				}
				if response.Update && response.Affected >= 0 {
					r.State, r.AffectedRows = contract.DataWriteAcknowledged, response.Affected
					result.Applied++
					result.Outcomes = append(result.Outcomes, contract.DataEditOutcome{Kind: snapshot.Edits[i].Kind, Original: contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: []contract.DataEdit{snapshot.Edits[i]}}).Edits[0].Original})
					continue
				}
			}
			if queryErr == nil {
				queryErr = contract.ErrUnsupported
			}
			return queryErr
		}
		return nil
	})
	result.Incomplete = result.Applied != len(snapshot.Edits)
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
	if l.writePlan == p {
		l.writePlan = nil
	}
	p.releaseLocked()
	l.mu.Unlock()
	return result, err
}

// Missing/null fields never become a zero-code acknowledgement. Failed native
// responses need the original request/action/code, success additionally needs
// an explicit update flag and affected count. Preserve malformed raw receipts.
func nativeDataResponse(raw []byte, request uint64, out *nativeResponse) bool {
	var r struct {
		Code     *int    `json:"code"`
		Action   *string `json:"action"`
		Request  *uint64 `json:"req_id"`
		Update   *bool   `json:"is_update"`
		Affected *int64  `json:"affected_rows"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Code == nil || r.Action == nil || *r.Action != "binary_query" || r.Request == nil || *r.Request != request || json.Unmarshal(raw, out) != nil {
		return false
	}
	return *r.Code != 0 || r.Update != nil && *r.Update && r.Affected != nil && *r.Affected >= 0
}

package databend

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
	"github.com/nyakang/nyadb/internal/services/connections"
)

const dataWritePlanMaxBytes = 32 << 20

type dataStatement struct {
	SQL, Where string
	Grant      dataGrant `json:"-"`
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
func (l *dataLease) writeAdmission() error {
	if l.closed {
		return contract.ErrRevoked
	}
	if l.writeBlocked {
		return contract.ErrDataCommitUncertain
	}
	return nil
}

func (l *dataLease) previewEdits(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
	if ctx == nil || len(edits) == 0 || len(edits) > contract.DataMaxPageSize {
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
	captured := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Object: l.object, Edits: edits})
	raw, err := json.Marshal(captured)
	if err != nil || len(raw) > dataWritePlanMaxBytes {
		return nil, contract.ErrDataEditLimit
	}
	snapshotBytes := len(raw)
	var plan *dataWritePlan
	err = l.withMatchingOriginal(ctx, func(child context.Context, original *ManagedSession) error {
		m, err := l.session.readTableMetadata(child, original, l.object)
		if err != nil {
			return err
		}
		if !dataHasRowID(m) {
			return contract.ErrUnsupported
		}
		var statements []dataStatement
		seen := map[string]bool{}
		for _, e := range captured.Edits {
			s, err := l.prepareDataStatement(m, e)
			if err != nil {
				return err
			}
			entity := ""
			if e.Original != nil {
				entity = s.Grant.RowID
				if seen[entity] {
					return connections.ErrInvalid
				}
				seen[entity] = true
				if err = l.checkOriginalEdit(child, original, m, s, e, false); err != nil {
					return err
				}
			}
			statements = append(statements, s)
			captured.Items = append(captured.Items, contract.DataMutationItem{Kind: e.Kind, EntityID: entity, Destination: l.object})
		}
		metadata := dataMetadataSignature(m)
		raw, err = json.Marshal(struct {
			Object        contract.ObjectRef
			Metadata      string
			Incarnation   string
			Statements    []dataStatement
			Scope         string
			OptimizerSkip string
		}{l.object, metadata, m.Incarnation, statements, "Independent native statements; original field text digests and same-statement uniqueness guard; no transaction or concurrent isolation guarantee", "PushDownFilterScan"})
		if err != nil || len(raw)+snapshotBytes > dataWritePlanMaxBytes {
			return contract.ErrDataEditLimit
		}
		captured.NativeJSON = string(raw)
		plan = &dataWritePlan{owner: l, snapshot: captured, statements: statements, metadata: metadata}
		return nil
	})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.writeAdmission(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.plan = plan
	return plan, nil
}

func (l *dataLease) prepareDataStatement(m *contract.TableStructure, e contract.DataEdit) (dataStatement, error) {
	s := dataStatement{}
	if !dataHasRowID(m) || e.GraphEndpoints != nil {
		return s, contract.ErrUnsupported
	}
	if e.Kind != "insert" && e.Kind != "update" && e.Kind != "delete" {
		return s, contract.ErrUnsupported
	}
	if e.Kind == "insert" && e.Original != nil || e.Kind != "insert" && e.Original == nil {
		return s, connections.ErrInvalid
	}
	if e.Kind == "delete" && len(e.Values) != 0 || e.Kind != "delete" && len(e.Values) == 0 {
		return s, connections.ErrInvalid
	}
	bytes := 0
	for name, v := range e.Values {
		i := slices.IndexFunc(m.Columns, func(c contract.StructureColumn) bool { return c.Name == name })
		if i < 0 || m.Columns[i].Generated || contract.DataColumnGenerated(m.Columns[i]) || name == "_row_id" {
			return s, contract.ErrUnsupported
		}
		bytes += len(v.Text) + len(v.Bytes)
	}
	if bytes > contract.DataEditMaxBytes {
		return s, contract.ErrDataEditLimit
	}
	if e.Original != nil {
		g, err := l.grant(e.Original.Ref)
		if err != nil {
			return s, err
		}
		if g.Metadata != dataMetadataSignature(m) || len(g.Digests) != len(m.Columns) || e.Original.Metadata != string(e.Original.Ref.Values[1]) || e.Original.Mode != "values" || len(e.Original.Fingerprints) != 0 || len(e.Original.Original) != len(m.Columns) || len(e.Original.Columns) != len(m.Columns) {
			return s, contract.ErrDataConflict
		}
		completeBytes := 0
		for i, c := range m.Columns {
			if c.Name != e.Original.Columns[i] {
				return s, contract.ErrDataConflict
			}
			v := e.Original.Original[i]
			if e.Kind == "update" && v.Truncated {
				return s, contract.ErrDataEditLimit
			}
			completeBytes += len(v.Text) + len(v.Bytes)
			if next, ok := e.Values[c.Name]; ok {
				completeBytes += len(next.Text) + len(next.Bytes) - len(v.Text) - len(v.Bytes)
			}
		}
		if e.Kind == "update" && completeBytes > contract.DataEditMaxBytes {
			return s, contract.ErrDataEditLimit
		}
		s.Grant = g
		where, err := dataGrantWhere(m, g)
		if err != nil {
			return s, err
		}
		s.Where = where
	}
	var columns, literals, assignments []string
	for _, c := range m.Columns {
		v, ok := e.Values[c.Name]
		if !ok {
			continue
		}
		t, err := parseDataType(c.Type)
		if err != nil {
			return s, err
		}
		t.nullable = c.Nullable
		literal, err := dataLiteral(t, v)
		if err != nil {
			return s, err
		}
		columns = append(columns, quoteName(c.Name))
		literals = append(literals, literal)
		assignments = append(assignments, quoteName(c.Name)+"="+literal)
	}
	name := qualified(l.object)
	guard := s.Where + " AND (SELECT count() FROM " + name + " WHERE " + s.Where + ")=1"
	switch e.Kind {
	case "insert":
		s.SQL = "INSERT INTO " + name + "(" + strings.Join(columns, ",") + ") VALUES(" + strings.Join(literals, ",") + ")"
	case "update":
		s.SQL = "UPDATE " + name + " SET " + strings.Join(assignments, ",") + " WHERE " + guard
	case "delete":
		s.SQL = "DELETE FROM " + name + " WHERE " + guard
	}
	return s, nil
}

// Databend forbids hidden _row_id in UPDATE/DELETE predicates. Native text
// digests cover all original fields and preserve SQL NULL separately. They are
// private signed grants, never generic raw-binary fingerprint assertions.
func dataGrantWhere(m *contract.TableStructure, g dataGrant) (string, error) {
	if len(g.Digests) != len(m.Columns) {
		return "", contract.ErrDataConflict
	}
	var predicates []string
	for i, c := range m.Columns {
		column := quoteName(c.Name)
		if g.Digests[i] == nil {
			predicates = append(predicates, column+" IS NULL")
			continue
		}
		text, err := dataText(c, column)
		if err != nil {
			return "", err
		}
		predicates = append(predicates, "sha2("+text+",256)="+quoteText(*g.Digests[i]))
	}
	return strings.Join(predicates, " AND "), nil
}

func (l *dataLease) checkOriginalEdit(ctx context.Context, original *ManagedSession, m *contract.TableStructure, statement dataStatement, e contract.DataEdit, afterOwnWrite bool) error {
	complete := e.Kind == "update"
	if e.Kind == "delete" {
		complete = true
		for _, c := range e.Original.Original {
			complete = complete && !c.Truncated
		}
	}
	limit := dataPreviewUnits
	if complete {
		limit = contract.DataEditMaxBytes
	}
	source, total, err := dataSource(m, true, complete)
	if err != nil {
		return err
	}
	p, err := newDataProjection(m, nil, limit, 0, true, complete, total)
	if err != nil {
		return err
	}
	// A FUSE UPDATE rewrites its block and changes even untouched rows' hidden
	// IDs. Only after this batch's acknowledged prefix do we locate the suffix
	// by its signed original field match. Initial previews/submits and field
	// reads still require the captured row ID. This is not concurrency isolation.
	where := quoteName(dataReadAlias) + `."_row_id"=` + statement.Grant.RowID
	if afterOwnWrite {
		where = statement.Where
	}
	query := "SELECT " + strings.Join(p.selects, ",") + " FROM " + source + " WHERE " + where + " LIMIT 2"
	rows := 0
	err = l.readRows(ctx, original, query, p, func(cells []contract.Cell, digests []*string, _ []int64, id string) error {
		rows++
		if rows != 1 {
			return contract.ErrDataConflict
		}
		matching := statement.Grant
		if afterOwnWrite {
			matching.RowID = id
		}
		if err := validateDataGrant(matching, m, digests, id); err != nil {
			return err
		}
		for i, c := range cells {
			observed := e.Original.Original[i]
			if !reflect.DeepEqual(c, observed) {
				return contract.ErrDataConflict
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return contract.ErrDataConflict
	}
	q, err := l.session.query(ctx, original, "SELECT count() FROM "+qualified(l.object)+" WHERE "+statement.Where, 1)
	if err != nil {
		return err
	}
	if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 || q.Sets[0].Rows[0][0].Kind != contract.Integer || q.Sets[0].Rows[0][0].Text != "1" {
		return contract.ErrDataConflict
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
	if l.plan != p || p.closed || p.consumed {
		l.mu.Unlock()
		return nil, connections.ErrInvalid
	}
	p.consumed = true
	snapshot := contract.CloneDataMutationSnapshot(p.snapshot)
	statements := slices.Clone(p.statements)
	metadata := p.metadata
	l.mu.Unlock()
	result := &contract.DataEditResult{Sequential: &contract.DataSequentialWriteReceipt{Object: l.object}}
	for i, e := range snapshot.Edits {
		result.Sequential.Statements = append(result.Sequential.Statements, contract.DataWriteStatementReceipt{EditIndex: i, Kind: e.Kind, Destination: l.object, State: contract.DataWriteNotSent})
	}
	conflict := false
	err := l.withMatchingOriginal(ctx, func(child context.Context, original *ManagedSession) error {
		for i, s := range statements {
			if err := child.Err(); err != nil {
				return err
			}
			m, err := l.session.readTableMetadata(child, original, l.object)
			if err != nil {
				return err
			}
			if dataMetadataSignature(m) != metadata || !dataHasRowID(m) {
				return contract.ErrDataConflict
			}
			if snapshot.Edits[i].Original != nil {
				if err = l.checkOriginalEdit(child, original, m, s, snapshot.Edits[i], result.Applied > 0); err != nil {
					return err
				}
			}
			r, queryErr := original.Call(child, "execute_query", map[string]any{"sql": s.SQL, "maxRows": 1, "timeoutSecs": l.session.profile.DatabendOptions.QueryTimeoutSeconds})
			l.session.observed("data-write", r)
			decoded, decodeErr := DecodeMutationReceipt(snapshot.Edits[i].Kind, r)
			out := &result.Sequential.Statements[i]
			out.State, out.AffectedRows = decoded.State, decoded.AffectedRows
			if r.Dispatched {
				raw, _ := json.Marshal(struct {
					Receipt  agentbridge.Receipt
					Original json.RawMessage
				}{r, r.OriginalResponse()})
				out.NativeJSON = string(raw)
			}
			if out.State != contract.DataWriteAcknowledged {
				return errors.Join(queryErr, decodeErr, contract.ErrDataConflict)
			}
			result.Applied++
			result.Outcomes = append(result.Outcomes, contract.DataEditOutcome{Kind: snapshot.Edits[i].Kind, Original: snapshot.Edits[i].Original})
			if out.AffectedRows != 1 {
				conflict = true
				return errors.Join(queryErr, contract.ErrDataConflict)
			}
			if queryErr != nil {
				conflict = true
				return queryErr
			}
		}
		return nil
	})
	result.Incomplete = conflict || err != nil || result.Applied != len(snapshot.Edits)
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

// In v1.2.881, filter pushdown evaluates an all-empty Map's statistics as Null
// and panics while converting it to Variant. Skip only that rule on this
// captured operation connection; restore the original setting before returning
// it to the pool. No profile-wide setting or SQL document is changed.
func (l *dataLease) withMatchingOriginal(ctx context.Context, fn func(context.Context, *ManagedSession) error) error {
	return l.withOriginal(ctx, false, func(child context.Context, original *ManagedSession) (err error) {
		q, err := l.session.query(child, original, "SELECT value FROM system.settings WHERE name='optimizer_skip_list'", 1)
		if err != nil {
			return err
		}
		if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 || q.Sets[0].Rows[0][0].Kind != contract.Text {
			return contract.ErrUnsupported
		}
		previous := q.Sets[0].Rows[0][0].Text
		for _, rule := range strings.Split(previous, ",") {
			if strings.TrimSpace(rule) == "PushDownFilterScan" {
				return fn(child, original)
			}
		}
		next := previous
		if next != "" {
			next += ","
		}
		next += "PushDownFilterScan"
		// Even a canceled SET may have taken effect. Restoration belongs to
		// the captured original executor and is attempted before logical close.
		defer func() {
			cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
			defer done()
			_, restoreErr := l.session.query(cleanup, original, "SET optimizer_skip_list="+quoteText(previous), 1)
			err = errors.Join(err, restoreErr)
		}()
		if _, err = l.session.query(child, original, "SET optimizer_skip_list="+quoteText(next), 1); err != nil {
			return err
		}
		return fn(child, original)
	})
}

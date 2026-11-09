package neo4j

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	nd "github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/nyakang/nyadb/internal/db/contract"
)

const dataWriteReceiptBytes = 8 << 20

type dataWriteStatement struct {
	Query      string
	Parameters []Property
	Result     Response
}

type dataWriteReceipt struct {
	Object                                               contract.ObjectRef
	Edits                                                []contract.DataEdit
	Statements                                           []dataWriteStatement
	BeginConfirmed, CommitAttempted, CommitConfirmed     bool
	RollbackAttempted, RollbackConfirmed, OutcomeUnknown bool
	Bookmarks                                            []string
	FailureCode                                          string
	CleanupFailed                                        bool
}

func (l *dataLease) save(ctx context.Context, id int64, db, name string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	prepared, err := l.prepareDataEdits(edits)
	if err != nil {
		return nil, err
	}
	return l.savePreparedEdits(ctx, prepared)
}

func (l *dataLease) savePreparedEdits(ctx context.Context, prepared []preparedDataEdit) (*contract.DataEditResult, error) {
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	writeLost := l.writeLost
	l.mu.Unlock()
	if writeLost {
		return nil, contract.ErrRevoked
	}
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	return l.saveTransaction(ctx, c, prepared)
}

// A grid batch uses one explicit native transaction, never a managed/retrying
// transaction. It locks each entity, validates its original complete fingerprint
// and typed payload, then applies that row's property map. Any later conflict
// rolls back the earlier provisional rows. Only Commit acknowledges completion.
func (l *dataLease) saveTransaction(parent context.Context, c *Client, prepared []preparedDataEdit) (out *contract.DataEditResult, err error) {
	c.operation.Lock()
	defer c.operation.Unlock()
	if !c.Alive() {
		return nil, ErrRetired
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(c.config.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	noRetry := true
	session := c.driver.NewSession(ctx, nd.SessionConfig{DatabaseName: l.object.Catalog, AccessMode: nd.AccessModeWrite, FetchSize: 2, DisableAutoCommitRetries: &noRetry})
	receipt := dataWriteReceipt{Object: l.object}
	for _, p := range prepared {
		receipt.Edits = append(receipt.Edits, p.edit)
	}
	out = &contract.DataEditResult{}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		if closeErr := session.Close(cleanup); closeErr != nil {
			receipt.CleanupFailed = true
		}
		receipt.OutcomeUnknown = out.CommitUncertain
		if out.CommitUncertain {
			l.mu.Lock()
			l.writeLost = true
			l.mu.Unlock()
		}
		if err != nil {
			var native *nd.Neo4jError
			if errors.As(err, &native) {
				receipt.FailureCode = native.Code
			}
		}
		raw, marshalErr := json.Marshal(receipt)
		if marshalErr != nil || len(raw) > dataWriteReceiptBytes {
			err = errors.Join(err, ErrBounds)
			out.Incomplete = true
			l.mu.Lock()
			l.writeLost = true
			l.mu.Unlock()
			return
		}
		out.NativeJSON = string(raw)
	}()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	tx, err := session.BeginTransaction(ctx, func(config *nd.TransactionConfig) {
		config.Timeout = time.Duration(c.config.RequestTimeoutSeconds) * time.Second
	})
	if err != nil {
		return out, err
	}
	receipt.BeginConfirmed = true
	transactionFailed := false
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		if !receipt.CommitAttempted {
			receipt.RollbackAttempted = true
			rollbackErr := tx.Rollback(cleanup)
			// The driver returns nil without sending ROLLBACK after a native
			// statement error or a failed connection. Nil in that state cannot
			// supply the missing server acknowledgement.
			receipt.RollbackConfirmed = rollbackErr == nil && !transactionFailed && ctx.Err() == nil
			if !receipt.RollbackConfirmed && len(receipt.Statements) > 0 {
				out.CommitUncertain = true
			}
			err = errors.Join(err, rollbackErr)
		}
		if closeErr := tx.Close(cleanup); closeErr != nil {
			receipt.CleanupFailed = true
		}
	}()

	// Sorting lock acquisition avoids avoidable inverted-lock deadlocks. Native
	// deadlock errors are still retained and never retried. Outcomes stay in the
	// caller's original row order.
	order := make([]int, len(prepared))
	for index := range order {
		order[index] = index
	}
	slices.SortFunc(order, func(a, b int) int {
		if prepared[a].identity.ElementID < prepared[b].identity.ElementID {
			return -1
		}
		if prepared[a].identity.ElementID > prepared[b].identity.ElementID {
			return 1
		}
		return 0
	})
	out.Outcomes = make([]contract.DataEditOutcome, len(prepared))
	previewBudget := dataWriteReceiptBytes
	for _, index := range order {
		p := prepared[index]
		if p.edit.Kind != "update" {
			outcome, runErr := l.runEntityEdit(ctx, c, tx, &receipt, p, &previewBudget)
			if runErr != nil {
				transactionFailed = !localDataWriteError(runErr)
				return out, runErr
			}
			out.Outcomes[index] = outcome
			continue
		}
		lockQuery := l.match() + " AND elementId(entity)=$element_id SET entity[$lock_key]=entity[$lock_key] RETURN entity LIMIT 2"
		locked, runErr := l.runDataWrite(ctx, c, tx, &receipt, lockQuery, map[string]any{"name": l.object.Name, "element_id": p.identity.ElementID, "lock_key": p.keys[0]})
		if runErr != nil {
			transactionFailed = !localDataWriteError(runErr)
			return out, runErr
		}
		if err = l.validateLockedDataEdit(p, locked); err != nil {
			return out, err
		}
		updateQuery := l.match() + " AND elementId(entity)=$element_id SET entity += $properties RETURN entity LIMIT 2"
		saved, runErr := l.runDataWrite(ctx, c, tx, &receipt, updateQuery, map[string]any{"name": l.object.Name, "element_id": p.identity.ElementID, "properties": p.params})
		if runErr != nil {
			transactionFailed = !localDataWriteError(runErr)
			return out, runErr
		}
		if err = l.validateSavedDataEdit(p, locked, saved); err != nil {
			return out, err
		}
		previews, snapshot, previewErr := l.preview(saved, p.identity.Properties, &previewBudget)
		if previewErr != nil {
			return out, previewErr
		}
		out.Outcomes[index] = contract.DataEditOutcome{Kind: "update", Original: p.edit.Original, Saved: snapshot, PreviewColumns: slices.Clone(snapshot.Columns), Previews: previews}
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	receipt.CommitAttempted = true
	if err = tx.Commit(ctx); err != nil {
		out.CommitUncertain = true
		return out, err
	}
	receipt.CommitConfirmed = true
	receipt.Bookmarks = slices.Clone(session.LastBookmarks())
	if len(receipt.Bookmarks) == 0 {
		out.CommitUncertain = true
		out.Incomplete = true
		return out, ErrProtocol
	}
	out.Applied, out.CommitConfirmed = len(prepared), true
	for _, p := range prepared {
		out.RefreshRequired = out.RefreshRequired || p.edit.Kind != "update"
	}
	return out, nil
}

func localDataWriteError(err error) bool {
	return errors.Is(err, ErrBounds) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrProtocol) || errors.Is(err, contract.ErrDataConflict)
}

func (l *dataLease) runDataWrite(ctx context.Context, c *Client, tx nd.ExplicitTransaction, receipt *dataWriteReceipt, query string, params map[string]any) (Value, error) {
	response, err := l.runDataStatement(ctx, c, tx, receipt, query, params, []string{"entity"})
	if err != nil {
		return Value{}, err
	}
	for name, count := range response.Summary.Counters {
		if name != "properties_set" && count != 0 {
			return Value{}, ErrProtocol
		}
	}
	return response.Rows[0][0].Clone(), nil
}

// Statement capture owns the original typed response, independently of both
// enclosing COMMIT and the mutation-specific identity/counter validation.
func (l *dataLease) runDataStatement(ctx context.Context, c *Client, tx nd.ExplicitTransaction, receipt *dataWriteReceipt, query string, params map[string]any, columns []string) (*Response, error) {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return nil, ErrInvalid
	}
	remaining := dataWriteReceiptBytes - len(raw) - 4096
	if remaining < 4096 {
		return nil, ErrBounds
	}
	plan, err := c.Prepare(Request{Database: l.object.Catalog, Query: query, Mode: "write", Parameters: params, MaxRows: 2, MaxBytes: min(2<<20, remaining), FetchSize: 2})
	if err != nil {
		return nil, err
	}
	statement := dataWriteStatement{Query: query, Parameters: plan.Parameters()}
	receipt.Statements = append(receipt.Statements, statement)
	statementIndex := len(receipt.Statements) - 1
	response := &receipt.Statements[statementIndex].Result
	response.Attempted = true
	result, err := tx.Run(ctx, query, plan.request.Parameters)
	if err != nil {
		response.Uncertain = true
		return nil, err
	}
	keys, err := result.Keys()
	if err != nil {
		return nil, err
	}
	response.Columns = slices.Clone(keys)
	if !slices.Equal(keys, columns) {
		_, _ = result.Consume(ctx)
		return nil, ErrProtocol
	}
	budget := valueBudget{limit: plan.request.MaxBytes}
	var captureErr error
	for result.Next(ctx) {
		record := result.Record()
		if record == nil || len(record.Values) != len(columns) || len(response.Rows) >= 2 {
			captureErr = ErrProtocol
			break
		}
		row := make([]Value, 0, len(columns))
		for _, native := range record.Values {
			value, _, valueErr := budget.capture(native, true, 0)
			if valueErr != nil {
				captureErr = valueErr
				response.Truncated = true
				break
			}
			row = append(row, value)
		}
		if captureErr != nil {
			break
		}
		response.Rows = append(response.Rows, row)
	}
	if err = result.Err(); err != nil {
		response.Uncertain = true
		return nil, err
	}
	summary, err := result.Consume(ctx)
	if err != nil {
		response.Uncertain = true
		return nil, err
	}
	if summary == nil || summary.Server() == nil || summary.Counters() == nil || summary.Database() == nil {
		return nil, ErrProtocol
	}
	observed := identity(summary.Server())
	if observed.Agent != c.identity.Agent || observed.ProtocolMajor != c.identity.ProtocolMajor || observed.ProtocolMinor != c.identity.ProtocolMinor || summary.Database().Name() != l.object.Catalog {
		return nil, ErrProtocol
	}
	counters := summary.Counters()
	response.Summary = Summary{Server: observed, Database: summary.Database().Name(), QueryType: summary.QueryType().String(), AvailableAfter: summary.ResultAvailableAfter(), ConsumedAfter: summary.ResultConsumedAfter(), Counters: map[string]int64{
		"nodes_created": int64(counters.NodesCreated()), "nodes_deleted": int64(counters.NodesDeleted()), "relationships_created": int64(counters.RelationshipsCreated()), "relationships_deleted": int64(counters.RelationshipsDeleted()), "properties_set": int64(counters.PropertiesSet()), "labels_added": int64(counters.LabelsAdded()), "labels_removed": int64(counters.LabelsRemoved()), "indexes_added": int64(counters.IndexesAdded()), "indexes_removed": int64(counters.IndexesRemoved()), "constraints_added": int64(counters.ConstraintsAdded()), "constraints_removed": int64(counters.ConstraintsRemoved()), "system_updates": int64(counters.SystemUpdates())}}
	response.Completed = true // Native statement only; enclosing Commit is separate.
	if captureErr != nil {
		return nil, captureErr
	}
	if len(response.Rows) != 1 {
		return nil, contract.ErrDataConflict
	}
	raw, err = json.Marshal(receipt)
	if err != nil || len(raw) > dataWriteReceiptBytes-4096 {
		response.Rows = nil
		response.Truncated = true
		return nil, ErrBounds
	}
	return response, nil
}

func (l *dataLease) validateSavedDataEdit(p preparedDataEdit, before, after Value) error {
	want := before.Clone()
	var properties *[]Property
	if want.Node != nil {
		properties = &want.Node.Properties
	} else if want.Relationship != nil {
		properties = &want.Relationship.Properties
	} else {
		return ErrProtocol
	}
	byKey := make(map[string]Value, len(*properties)+len(p.after))
	for _, property := range *properties {
		byKey[property.Name] = property.Value
	}
	for key, value := range p.after {
		if value.Kind == "null" {
			delete(byKey, key)
		} else {
			byKey[key] = value.Clone()
		}
	}
	names := make([]string, 0, len(byKey))
	for name := range byKey {
		names = append(names, name)
	}
	slices.Sort(names)
	*properties = make([]Property, 0, len(names))
	for _, name := range names {
		*properties = append(*properties, Property{Name: name, Value: byKey[name]})
	}
	id, err := l.entityID(after)
	if err != nil || id != p.identity.ElementID || !reflect.DeepEqual(want, after) {
		return ErrProtocol
	}
	return nil
}

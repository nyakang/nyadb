package databend

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

// Databend's original JDBC DDL acknowledgement is zero columns and one empty
// row. It has no affected-row count or transaction proof. Only this complete
// response acknowledges the single DDL; a later read cannot repair its loss.
func acknowledgedDDL(r agentbridge.Receipt) bool {
	if !r.Dispatched || !r.ExecutorAcknowledged || r.RemoteError != nil {
		return false
	}
	var v struct {
		Columns   *[]string            `json:"columns"`
		Types     *[]string            `json:"column_types"`
		Rows      *[][]json.RawMessage `json:"rows"`
		Affected  *int64               `json:"affected_rows"`
		Truncated *bool                `json:"truncated"`
	}
	return json.Unmarshal(r.Result, &v) == nil && v.Columns != nil && len(*v.Columns) == 0 && v.Types != nil && len(*v.Types) == 0 &&
		v.Rows != nil && len(*v.Rows) == 1 && (*v.Rows)[0] != nil && len((*v.Rows)[0]) == 0 && v.Affected != nil && *v.Affected == 0 && v.Truncated != nil && !*v.Truncated
}

func (s *Session) schemaNameAbsent(ctx context.Context, original *ManagedSession, o contract.ObjectRef) error {
	q, err := s.query(ctx, original, "SELECT to_string(table_id) AS __nyadb_table_id FROM system.tables WHERE database="+quoteText(o.Schema)+" AND name="+quoteText(o.Name), 2)
	if err != nil {
		return err
	}
	if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 1 || q.Sets[0].Columns[0].Name != "__nyadb_table_id" {
		return contract.ErrUnsupported
	}
	if len(q.Sets[0].Rows) != 0 {
		return schemaddl.ErrConflict
	}
	return nil
}

func (d *schemaDocument) rename(ctx context.Context, request contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
	return d.writeSchema(ctx, contract.SchemaCopyRequest{Original: request.Original.Clone(), Destination: request.Destination}, "rename")
}

func (d *schemaDocument) drop(ctx context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	return d.writeSchema(ctx, contract.SchemaCopyRequest{Original: original.Clone(), Destination: original.Target.Object.Name}, "drop")
}

func (d *schemaDocument) copy(ctx context.Context, request contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
	return d.writeSchema(ctx, request.Clone(), "copy")
}

// All single-statement operations retain the same original document, proof and
// settlement rules. The operation selects a fixed planner, never arbitrary SQL.
func (d *schemaDocument) writeSchema(ctx context.Context, request contract.SchemaCopyRequest, operation string) (out contract.SchemaDesignOutcome) {
	r := request.Clone()
	out.State = "not_executed"
	child, done, err := d.admission(ctx, r.Original.Target)
	if err != nil {
		out.Error = err
		return
	}
	defer done()
	if !d.writable(r.Original.Target) {
		out.Error = contract.ErrSchemaCommitUncertain
		return
	}
	var sql string
	switch operation {
	case "rename":
		sql, err = schemaddl.PlanDatabendRename(r.Original.Schema, r.Original.Target.Object.Schema, r.Destination)
	case "copy":
		sql, err = schemaddl.PlanDatabendCopy(r.Original.Schema, r.Original.Target.Object.Schema, r.Destination, r.WithData, r.RebindSelfReferences, r.PreserveRowID)
		if err == nil {
			for _, c := range r.Original.Schema.Databend.Columns {
				if typ, typeErr := parseDataType(c.Type); typeErr != nil || typ.kind == contract.JSON || typ.kind == contract.Floating {
					err = fmt.Errorf("%w: copy verification for Databend column %q (%s)", contract.ErrUnsupported, c.Name, c.Type)
					break
				}
			}
		}
	case "clear":
		sql, err = schemaddl.PlanDatabendClear(r.Original.Schema, r.Original.Target.Object.Schema, false)
	case "truncate":
		sql, err = schemaddl.PlanDatabendTruncate(r.Original.Schema, r.Original.Target.Object.Schema)
	case "drop":
		sql, err = schemaddl.PlanDatabendDrop(r.Original.Schema, r.Original.Target.Object.Schema)
	default:
		err = contract.ErrUnsupported
	}
	if err != nil {
		out.Error = err
		return
	}
	if !hmac.Equal([]byte(r.Original.Schema.Revision), []byte(sealSchema(r.Original, d.key[:]))) {
		out.Error = schemaddl.ErrConflict
		return
	}
	original, receipts, err := d.s.process.OpenSession(child, false)
	for _, receipt := range receipts {
		d.s.observed("schema-open", receipt)
	}
	if err != nil {
		out.Error = err
		return
	}
	defer func() {
		receipt, closeErr := original.Close()
		d.s.observed("schema-close", receipt)
		// A close error cannot erase a settled original DDL acknowledgement.
		if out.Receipt == nil {
			out.Error = errors.Join(out.Error, closeErr)
		}
	}()
	current, err := d.s.readSchemaSnapshot(child, original, r.Original.Target, d.key[:])
	if err != nil {
		out.Error = err
		return
	}
	if !reflect.DeepEqual(current, r.Original) {
		out.Error = schemaddl.ErrConflict
		return
	}
	destination := r.Original.Target
	destination.Object.Name = r.Destination
	if operation == "rename" || operation == "copy" {
		if err = d.s.schemaNameAbsent(child, original, destination.Object); err != nil {
			out.Error = err
			return
		}
	}
	// These observations are prechecks, not concurrent DDL locks. Verification
	// below checks native incarnation and all declaration bytes independently.
	if err = child.Err(); err != nil {
		out.Error = err
		return
	}
	if err = d.s.checkSchemaTarget(r.Original.Target); err != nil {
		out.Error = err
		return
	}
	receipt, callErr := original.Call(child, "execute_query", map[string]any{"sql": sql, "maxRows": 1, "timeoutSecs": d.s.profile.DatabendOptions.QueryTimeoutSeconds})
	d.s.observed("schema-"+operation, receipt)
	if !receipt.Dispatched {
		out.Error = errors.Join(callErr, agentbridge.ErrNativeResult)
		return
	}
	out.State = "uncertain"
	var decodeErr error
	if operation == "clear" {
		var mutation MutationReceipt
		mutation, decodeErr = DecodeMutationReceipt("delete", receipt)
		if mutation.State == contract.DataWriteAcknowledged {
			out.State, out.Rows = "committed", mutation.AffectedRows
		} else if mutation.State == contract.DataWriteFailed {
			out.State = "not_executed"
		}
	} else if acknowledgedDDL(receipt) {
		out.State = "committed"
	} else if receipt.ExecutorAcknowledged && receipt.RemoteError != nil && receipt.RemoteError.OperationOutcome == "not_started" {
		out.State = "not_executed"
	}
	out.Error = errors.Join(callErr, decodeErr)
	if out.State == "committed" && operation == "rename" {
		actual, readErr := d.s.readSchemaSnapshot(child, original, destination, d.key[:])
		if readErr == nil {
			before, _ := schemaddl.DatabendTableBody(r.Original.Schema.DDL, r.Original.Schema.Table.Name)
			after, bodyErr := schemaddl.DatabendTableBody(actual.Schema.DDL, actual.Schema.Table.Name)
			readErr = bodyErr
			if actual.Schema.Databend.TableID != r.Original.Schema.Databend.TableID || before != after {
				readErr = schemaddl.ErrConflict
			}
			if readErr == nil {
				readErr = d.s.schemaNameAbsent(child, original, r.Original.Target.Object)
			}
			if readErr == nil && child.Err() == nil && callErr == nil {
				out.Actual = &actual
				out.Verified = true
			}
		}
		out.Error = errors.Join(out.Error, readErr, child.Err())
	}
	if out.State == "committed" && (operation == "clear" || operation == "truncate") {
		actual, readErr := d.verifyClear(child, original, r.Original)
		out.Error = errors.Join(out.Error, readErr, child.Err())
		if out.Error == nil {
			out.Actual, out.Verified = &actual, true
		}
	}
	if out.State == "committed" && operation == "copy" {
		actual, readErr := d.verifyCopy(child, original, r, destination)
		out.Error = errors.Join(out.Error, readErr, child.Err())
		if out.Error == nil {
			out.Actual, out.Verified = &actual, true
		}
	}
	if out.State == "committed" && operation == "drop" {
		readErr := d.s.schemaNameAbsent(child, original, r.Original.Target.Object)
		if readErr == nil {
			readErr = d.s.schemaIncarnationAbsent(child, original, r.Original.Schema.Databend.TableID)
		}
		out.Error = errors.Join(out.Error, readErr, child.Err())
		if out.Error == nil {
			actual := r.Original.Clone()
			actual.Schema.Exists, actual.Schema.DDL = false, ""
			actual.Schema.Revision = sealSchema(actual, d.key[:])
			out.Actual, out.Verified = &actual, true
		}
	}
	if out.State == "uncertain" || out.State == "committed" && !out.Verified {
		out.Error = errors.Join(out.Error, contract.ErrSchemaCommitUncertain)
	}
	raw, _ := json.Marshal(struct {
		Receipt  agentbridge.Receipt
		Original json.RawMessage
	}{receipt, receipt.OriginalResponse()})
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sequence++
	out.Receipt = &contract.SchemaDesignWriteReceipt{DocumentID: d.id, Sequence: d.sequence, Operation: operation, Original: r.Original.Clone(), Destination: r.Destination, WithData: r.WithData, RebindSelfReferences: r.RebindSelfReferences, PreserveRowID: r.PreserveRowID, Statements: []string{sql}, NativeJSON: string(raw), State: out.State, Verified: out.Verified, Rows: out.Rows, Actual: out.Actual}
	retained := out.Clone()
	d.original = &retained
	if out.State == "uncertain" || out.State == "committed" && !out.Verified {
		d.blocked = true
	}
	if out.Verified && !d.closed && operation == "rename" {
		d.target = destination
	}
	return out.Clone()
}

// A successful DROP must also remove the captured incarnation from the active
// catalog. A concurrent rename must not masquerade as an absent original.
func (s *Session) schemaIncarnationAbsent(ctx context.Context, original *ManagedSession, id string) error {
	q, err := s.query(ctx, original, "SELECT to_string(table_id) AS __nyadb_table_id FROM system.tables WHERE to_string(table_id)="+quoteText(id), 2)
	if err != nil {
		return err
	}
	if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 1 || q.Sets[0].Columns[0].Name != "__nyadb_table_id" {
		return contract.ErrUnsupported
	}
	if len(q.Sets[0].Rows) != 0 {
		return schemaddl.ErrConflict
	}
	return nil
}

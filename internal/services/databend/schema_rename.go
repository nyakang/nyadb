package databend

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
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

func (d *schemaDocument) rename(ctx context.Context, request contract.SchemaRenameRequest) (out contract.SchemaDesignOutcome) {
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
	sql, err := schemaddl.PlanDatabendRename(r.Original.Schema, r.Original.Target.Object.Schema, r.Destination)
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
	if err = d.s.schemaNameAbsent(child, original, destination.Object); err != nil {
		out.Error = err
		return
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
	d.s.observed("schema-rename", receipt)
	if !receipt.Dispatched {
		out.Error = errors.Join(callErr, agentbridge.ErrNativeResult)
		return
	}
	out.State = "uncertain"
	if acknowledgedDDL(receipt) {
		out.State = "committed"
	} else if receipt.ExecutorAcknowledged && receipt.RemoteError != nil && receipt.RemoteError.OperationOutcome == "not_started" {
		out.State = "not_executed"
	}
	out.Error = callErr
	if out.State == "committed" {
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
	out.Receipt = &contract.SchemaDesignWriteReceipt{DocumentID: d.id, Sequence: d.sequence, Operation: "rename", Original: r.Original.Clone(), Destination: r.Destination, Statements: []string{sql}, NativeJSON: string(raw), State: out.State, Verified: out.Verified, Actual: out.Actual}
	retained := out.Clone()
	d.original = &retained
	if out.State == "uncertain" || out.State == "committed" && !out.Verified {
		d.blocked = true
	}
	if out.Verified && !d.closed {
		d.target = destination
	}
	return out.Clone()
}

package databend

import (
	"context"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (d *schemaDocument) clear(ctx context.Context, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
	r := request.Clone()
	if r.ResetIdentity {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
	}
	return d.writeSchema(ctx, contract.SchemaCopyRequest{Original: r.Original, Destination: r.Original.Target.Object.Name}, "clear")
}

func (d *schemaDocument) verifyClear(ctx context.Context, original *ManagedSession, before contract.SchemaDesignSnapshot) (contract.SchemaDesignSnapshot, error) {
	var empty contract.SchemaDesignSnapshot
	current, err := d.s.readSchemaSnapshot(ctx, original, before.Target, d.key[:])
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(current, before) {
		return empty, schemaddl.ErrConflict
	}
	q, err := d.s.query(ctx, original, "SELECT to_string(count(*)) AS __nyadb_clear_remaining FROM "+qualified(before.Target.Object), 2)
	if err != nil {
		return empty, err
	}
	if q.Truncated || len(q.Sets) != 1 || len(q.Sets[0].Columns) != 1 || q.Sets[0].Columns[0].Name != "__nyadb_clear_remaining" || len(q.Sets[0].Rows) != 1 || len(q.Sets[0].Rows[0]) != 1 {
		return empty, contract.ErrUnsupported
	}
	cell := q.Sets[0].Rows[0][0]
	if cell.Kind != contract.Text || cell.Truncated || cell.Text != "0" {
		return empty, schemaddl.ErrDataChanged
	}
	current, err = d.s.readSchemaSnapshot(ctx, original, before.Target, d.key[:])
	if err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(current, before) {
		return empty, schemaddl.ErrConflict
	}
	return current, nil
}

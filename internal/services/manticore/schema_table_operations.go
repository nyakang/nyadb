package manticore

import (
	"context"
	"database/sql/driver"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

func (d *schemaDocument) drop(ctx context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	return d.tableOperation(ctx, original, "drop")
}
func (d *schemaDocument) truncate(ctx context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
	return d.tableOperation(ctx, original, "truncate")
}
func (d *schemaDocument) tableOperation(ctx context.Context, original contract.SchemaDesignSnapshot, operation string) (out contract.SchemaDesignOutcome) {
	original = original.Clone()
	out.State = "not_executed"
	child, done, err := d.admission(ctx, original.Target)
	if err != nil {
		out.Error = err
		return
	}
	defer done()
	if err = d.checkProof(original); err != nil {
		out.Error = err
		return
	}
	plan, err := schemaddl.PlanManticoreRTTableOperation(original.Schema, operation)
	if err != nil {
		out.Error = err
		return
	}
	receipt := contract.SchemaDesignWriteReceipt{Operation: operation, Original: original, Destination: original.Target.Object.Name, Statements: plan.SQL}
	return d.executeSchema(child, receipt, func(ctx context.Context) (contract.SchemaDesignSnapshot, error) {
		if operation == "drop" {
			if err := d.s.schemaNameAbsent(ctx, original.Target); err != nil {
				return contract.SchemaDesignSnapshot{}, err
			}
			actual := original.Clone()
			actual.Schema.Exists, actual.Schema.DDL = false, ""
			actual.Schema.Revision = sealSchema(actual, d.key[:])
			return actual, nil
		}
		actual, err := d.s.schemaSnapshot(ctx, original.Target, d.key[:])
		if err == nil {
			err = schemaddl.VerifyManticoreRT(original.Schema.Table, actual.Schema.Table)
		}
		if err == nil {
			err = d.s.schemaTableEmpty(ctx, original.Target)
		}
		return actual, err
	})
}
func (s *Session) schemaNameAbsent(ctx context.Context, target contract.SchemaDesignTarget) error {
	if err := s.checkSchemaTarget(target); err != nil {
		return err
	}
	return s.withConnection(ctx, func(child context.Context, c driver.Conn) error {
		set, err := read(child, c, "SHOW TABLES", 0, nil, nil)
		if err != nil {
			return err
		}
		objects, err := tableObjects(target.Object.Namespace(), set)
		if err != nil {
			return err
		}
		for _, object := range objects {
			if strings.EqualFold(object.Name, target.Object.Name) {
				return schemaddl.ErrConflict
			}
		}
		return child.Err()
	})
}
func (s *Session) schemaTableEmpty(ctx context.Context, target contract.SchemaDesignTarget) error {
	if err := s.checkSchemaTarget(target); err != nil {
		return err
	}
	return s.withConnection(ctx, func(child context.Context, c driver.Conn) error {
		set, err := read(child, c, "SELECT id FROM "+quote(target.Object.Name)+" LIMIT 1", 0, nil, nil)
		if err != nil {
			return err
		}
		if len(set.Truncation) != 0 || len(set.Columns) != 1 || set.Columns[0].Name != "id" {
			return contract.ErrUnsupported
		}
		if len(set.Rows) != 0 {
			return schemaddl.ErrDataChanged
		}
		return child.Err()
	})
}

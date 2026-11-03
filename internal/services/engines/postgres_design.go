package engines

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// CanApply validates only the captured session/scope and performs no I/O.
// Authoritative version, permission and declaration checks run on the worker.
func (s *Session) postgresDesignApply(ctx context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
	r := request.Clone()
	if err := s.checkPostgresDesign(r.Original.Target); err != nil {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
	}
	p := r.Original.Schema.Postgres
	if p == nil || p.ServerVersion < 140000 || p.ServerVersion >= 190000 {
		return contract.SchemaDesignOutcome{State: "not_executed", Error: schemaddl.ErrPostgreSQLVersion}
	}
	// Even rename/metadata paths must honor the released core-type scope.
	// The executor checks this complete before-image under its original lock.
	for _, entry := range p.Entries {
		if entry.Kind != "column" || entry.OID != p.RelationOID {
			continue
		}
		var column struct {
			Type    json.RawMessage `json:"atttypid"`
			Dropped bool            `json:"attisdropped"`
		}
		if json.Unmarshal(entry.Data, &column) != nil {
			return contract.SchemaDesignOutcome{State: "not_executed", Error: schemaddl.ErrConflict}
		}
		if column.Dropped {
			continue
		}
		var oid int64
		if json.Unmarshal(column.Type, &oid) != nil {
			var text string
			if json.Unmarshal(column.Type, &text) != nil || json.Unmarshal([]byte(text), &oid) != nil {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: schemaddl.ErrConflict}
			}
		}
		if oid <= 0 || oid >= 16384 {
			return contract.SchemaDesignOutcome{State: "not_executed", Error: schemaddl.ErrPostgreSQLCoreTypes}
		}
	}
	out := s.postgresDesignDDLApply(ctx, r)
	if out.State == "uncertain" {
		out.Error = errors.Join(out.Error, s.Close())
	}
	return out
}

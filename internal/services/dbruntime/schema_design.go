package dbruntime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

// SchemaDesign binds the optional feature to this published generation. The
// host still controls product capability exposure; an absent adapter returns
// an empty binding rather than unwrapping into an unguarded raw provider.
func (s *session) SchemaDesign() contract.SchemaDesignOperations {
	if !s.alive() {
		return contract.SchemaDesignOperations{}
	}
	p, err := contract.Require[contract.SchemaDesignProvider](s.raw)
	if err != nil {
		return contract.SchemaDesignOperations{}
	}
	return s.bindSchemaDesign(p.SchemaDesign(), "")
}

func (s *session) bindSchemaDesign(raw contract.SchemaDesignOperations, documentID string) contract.SchemaDesignOperations {
	ops := contract.SchemaDesignOperations{}
	if raw.Plan != nil {
		ops.Plan = func(request contract.SchemaDesignRequest) (schemaddl.Plan, error) {
			if err := s.schemaDesignTarget(request.Original.Target); err != nil {
				return schemaddl.Plan{}, err
			}
			if !s.Capabilities().DesignTables {
				return schemaddl.Plan{}, contract.ErrUnsupported
			}
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			plan, err := raw.Plan(r)
			return plan.Clone(), err
		}
	}
	if raw.CanApply != nil {
		ops.CanApply = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil || !s.Capabilities().DesignTables {
				return false
			}
			t.Generation = s.raw.Generation()
			return raw.CanApply(t)
		}
	}
	if raw.Snapshot != nil {
		ops.Snapshot = func(ctx context.Context, t contract.SchemaDesignTarget) (contract.SchemaDesignSnapshot, error) {
			if err := s.schemaDesignTarget(t); err != nil {
				return contract.SchemaDesignSnapshot{}, err
			}
			t.Generation = s.raw.Generation()
			snapshot, err := raw.Snapshot(ctx, t)
			if err != nil {
				return snapshot, err
			}
			if snapshot.Target != t {
				return contract.SchemaDesignSnapshot{}, errors.New("schema provider returned another original scope")
			}
			if !s.alive() {
				return contract.SchemaDesignSnapshot{}, contract.ErrRevoked
			}
			snapshot = snapshot.Clone()
			snapshot.Target.Generation = s.generation
			return snapshot, nil
		}
	}
	if raw.Apply != nil {
		ops.Apply = func(ctx context.Context, request contract.SchemaDesignRequest) contract.SchemaDesignOutcome {
			if err := s.schemaDesignTarget(request.Original.Target); err != nil {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
			}
			if !s.Capabilities().DesignTables {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
			}
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			expected := contract.SchemaDesignWriteReceipt{DocumentID: documentID, Operation: "apply", Original: r.Original.Clone(), Desired: tablePointer(r.Desired), Destination: r.Desired.Name, AllowColumnDrop: r.AllowColumnDrop}
			return s.schemaDesignOutcome(raw.Apply(ctx, r.Clone()), expected)
		}
	}
	if raw.CanDrop != nil {
		ops.CanDrop = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil || !s.Capabilities().Objects {
				return false
			}
			t.Generation = s.raw.Generation()
			return raw.CanDrop(t)
		}
	}
	if raw.Drop != nil {
		ops.Drop = func(ctx context.Context, original contract.SchemaDesignSnapshot) contract.SchemaDesignOutcome {
			if err := s.schemaDesignTarget(original.Target); err != nil {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
			}
			if !s.Capabilities().Objects {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
			}
			r := original.Clone()
			r.Target.Generation = s.raw.Generation()
			expected := contract.SchemaDesignWriteReceipt{DocumentID: documentID, Operation: "drop", Original: r.Clone(), Destination: r.Target.Object.Name}
			return s.schemaDesignOutcome(raw.Drop(ctx, r.Clone()), expected)
		}
	}
	if raw.CanCopy != nil {
		ops.CanCopy = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil || !s.Capabilities().Objects {
				return false
			}
			t.Generation = s.raw.Generation()
			return raw.CanCopy(t)
		}
	}
	if raw.Copy != nil {
		ops.Copy = func(ctx context.Context, request contract.SchemaCopyRequest) contract.SchemaDesignOutcome {
			if err := s.schemaDesignTarget(request.Original.Target); err != nil {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
			}
			if !s.Capabilities().Objects {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
			}
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			expected := contract.SchemaDesignWriteReceipt{DocumentID: documentID, Operation: "copy", Original: r.Original.Clone(), Destination: r.Destination, WithData: r.WithData, RebindSelfReferences: r.RebindSelfReferences, PreserveRowID: r.PreserveRowID}
			return s.schemaDesignOutcome(raw.Copy(ctx, r.Clone()), expected)
		}
	}
	if raw.CanRename != nil {
		ops.CanRename = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil || !s.Capabilities().Objects {
				return false
			}
			t.Generation = s.raw.Generation()
			return raw.CanRename(t)
		}
	}
	if raw.CanClear != nil {
		ops.CanClear = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil || !s.Capabilities().Objects {
				return false
			}
			t.Generation = s.raw.Generation()
			return raw.CanClear(t)
		}
	}
	if raw.Clear != nil {
		ops.Clear = func(ctx context.Context, request contract.SchemaClearRequest) contract.SchemaDesignOutcome {
			if err := s.schemaDesignTarget(request.Original.Target); err != nil {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
			}
			if !s.Capabilities().Objects {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
			}
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			expected := contract.SchemaDesignWriteReceipt{DocumentID: documentID, Operation: "clear", Original: r.Original.Clone(), Destination: r.Original.Target.Object.Name, ResetIdentity: r.ResetIdentity}
			return s.schemaDesignOutcome(raw.Clear(ctx, r.Clone()), expected)
		}
	}
	if raw.Rename != nil {
		ops.Rename = func(ctx context.Context, request contract.SchemaRenameRequest) contract.SchemaDesignOutcome {
			if err := s.schemaDesignTarget(request.Original.Target); err != nil {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: err}
			}
			if !s.Capabilities().Objects {
				return contract.SchemaDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
			}
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			expected := contract.SchemaDesignWriteReceipt{DocumentID: documentID, Operation: "rename", Original: r.Original.Clone(), Destination: r.Destination}
			return s.schemaDesignOutcome(raw.Rename(ctx, r.Clone()), expected)
		}
	}
	return ops
}
func (s *session) schemaDesignTarget(t contract.SchemaDesignTarget) error {
	if !s.alive() || t.Generation == 0 || t.Generation != s.generation {
		return contract.ErrRevoked
	}
	return s.require(true, t.Object.ProfileID)
}

func tablePointer(t schemaddl.Table) *schemaddl.Table { t = t.Clone(); return &t }

// Translate only captured native targets. A late acknowledgement still belongs
// to this published generation after disconnect or a newer session opens.
func (s *session) schemaDesignOutcome(out contract.SchemaDesignOutcome, expected contract.SchemaDesignWriteReceipt) contract.SchemaDesignOutcome {
	out = out.Clone()
	target := expected.Original.Target
	target.Object.Name = expected.Destination
	validActual := func(actual *contract.SchemaDesignSnapshot) bool {
		if actual == nil || actual.Target != target {
			return false
		}
		switch expected.Operation {
		case "drop":
			return !actual.Schema.Exists
		case "copy":
			return actual.Schema.Exists
		case "rename":
			return actual.Schema.Exists && actual.Schema.Table.Name == expected.Destination
		case "clear":
			return actual.Schema.Exists && actual.Schema.Table.Name == expected.Destination && actual.Schema.Engine == expected.Original.Schema.Engine
		}
		if expected.DocumentID != "" {
			return actual.Schema.Exists && actual.Schema.Table.Name == expected.Destination && actual.Schema.Engine == expected.Original.Schema.Engine
		}
		return true // Preserve the legacy Apply provider's validation contract.
	}
	var validation error
	if expected.DocumentID != "" && out.Verified && (out.State != "committed" || out.Actual == nil || out.Error != nil) {
		validation = errors.New("verified schema commit has no complete successful original snapshot")
	}
	if out.Actual != nil && !validActual(out.Actual) {
		validation = errors.New("schema receipt differs from captured destination scope")
	}
	if r := out.Receipt; r != nil {
		if (r.State == "committed" || r.State == "rolled_back") && (r.NativeJSON == "" || !json.Valid([]byte(r.NativeJSON)) || len(r.Statements) == 0) {
			validation = errors.Join(validation, errors.New("original schema acknowledgement or executed statement array is incomplete"))
		}
		if r.DocumentID == "" || expected.DocumentID != "" && r.DocumentID != expected.DocumentID || r.Sequence == 0 ||
			r.Operation != expected.Operation || !reflect.DeepEqual(r.Original, expected.Original) ||
			!reflect.DeepEqual(r.Desired, expected.Desired) || r.Destination != expected.Destination ||
			r.AllowColumnDrop != expected.AllowColumnDrop || r.WithData != expected.WithData ||
			r.RebindSelfReferences != expected.RebindSelfReferences || r.PreserveRowID != expected.PreserveRowID || r.ResetIdentity != expected.ResetIdentity ||
			r.State != out.State || r.Verified != out.Verified || r.Rows != out.Rows || !reflect.DeepEqual(r.Actual, out.Actual) {
			validation = errors.Join(validation, errors.New("schema acknowledgement differs from captured original document or request"))
		}
		// Original evidence is retained even when validation fails. Never rewrite
		// a foreign profile/target to make it look like the captured request.
		if r.Original.Target == expected.Original.Target {
			r.Original.Target.Generation = s.generation
		}
		if r.Actual != nil && r.Actual.Target == target {
			r.Actual.Target.Generation = s.generation
		}
	} else if expected.DocumentID != "" && out.State != "not_executed" {
		validation = errors.Join(validation, contract.ErrSchemaCommitUncertain)
		out.State = "uncertain"
	}
	if validation != nil {
		out.Verified, out.Actual = false, nil
		out.Error = errors.Join(out.Error, validation)
		if expected.DocumentID != "" && out.State != "committed" {
			out.State = "uncertain"
		}
	} else if out.Actual != nil {
		out.Actual.Target.Generation = s.generation
	}
	return out
}
func (m *Manager) SchemaDesign(id int64) contract.SchemaDesignOperations {
	s, err := m.Session(id)
	if err != nil {
		return contract.SchemaDesignOperations{}
	}
	p, err := contract.Require[contract.SchemaDesignProvider](s)
	if err != nil {
		return contract.SchemaDesignOperations{}
	}
	return p.SchemaDesign()
}

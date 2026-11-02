package dbruntime

import (
	"context"
	"errors"

	"github.com/nyakang/nyadb/internal/db/contract"
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
	raw := p.SchemaDesign()
	ops := contract.SchemaDesignOperations{}
	if raw.CanApply != nil {
		ops.CanApply = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil {
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
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			out := raw.Apply(ctx, r)
			// A late commit belongs to the original scope. Preserve its receipt
			// even after revocation so the host can invalidate that original table.
			if out.Actual != nil {
				actual := out.Actual.Clone()
				expected := r.Original.Target
				expected.Object.Name = r.Desired.Name
				if actual.Target != expected {
					out.Verified = false
					out.Actual = nil
					out.Error = errors.Join(out.Error, errors.New("schema receipt differs from captured scope"))
					return out
				}
				actual.Target.Generation = s.generation
				out.Actual = &actual
			}
			return out
		}
	}
	if raw.CanDrop != nil {
		ops.CanDrop = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil {
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
			r := original.Clone()
			r.Target.Generation = s.raw.Generation()
			out := raw.Drop(ctx, r)
			if out.Actual != nil {
				actual := out.Actual.Clone()
				if actual.Target != r.Target || actual.Schema.Exists {
					out.Verified, out.Actual = false, nil
					out.Error = errors.Join(out.Error, errors.New("drop receipt differs from captured scope or table remains"))
					return out
				}
				actual.Target.Generation = s.generation
				out.Actual = &actual
			}
			return out
		}
	}
	if raw.CanCopy != nil {
		ops.CanCopy = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil {
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
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			out := raw.Copy(ctx, r)
			if out.Actual != nil {
				actual := out.Actual.Clone()
				expected := r.Original.Target
				expected.Object.Name = r.Destination
				if actual.Target != expected || !actual.Schema.Exists {
					out.Verified, out.Actual = false, nil
					out.Error = errors.Join(out.Error, errors.New("copy receipt differs from captured destination scope"))
					return out
				}
				actual.Target.Generation = s.generation
				out.Actual = &actual
			}
			return out
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

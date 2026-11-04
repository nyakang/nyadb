package dbruntime

import (
	"context"
	"errors"
	"github.com/nyakang/nyadb/internal/db/contract"
)

func (s *session) ViewDesign() contract.ViewDesignOperations {
	if !s.alive() {
		return contract.ViewDesignOperations{}
	}
	p, err := contract.Require[contract.ViewDesignProvider](s.raw)
	if err != nil {
		return contract.ViewDesignOperations{}
	}
	raw := p.ViewDesign()
	ops := contract.ViewDesignOperations{}
	if raw.CanApply != nil {
		ops.CanApply = func(t contract.SchemaDesignTarget) bool {
			if s.schemaDesignTarget(t) != nil || t.Object.Kind != "view" || !s.Capabilities().DesignViews {
				return false
			}
			t.Generation = s.raw.Generation()
			return raw.CanApply(t)
		}
	}
	if raw.Snapshot != nil {
		ops.Snapshot = func(ctx context.Context, t contract.SchemaDesignTarget) (contract.ViewDesignSnapshot, error) {
			if err := s.schemaDesignTarget(t); err != nil {
				return contract.ViewDesignSnapshot{}, err
			}
			t.Generation = s.raw.Generation()
			out, err := raw.Snapshot(ctx, t)
			if err != nil {
				return out, err
			}
			if out.Target != t {
				return contract.ViewDesignSnapshot{}, errors.New("view provider returned another original scope")
			}
			if !s.alive() {
				return contract.ViewDesignSnapshot{}, contract.ErrRevoked
			}
			out = out.Clone()
			out.Target.Generation = s.generation
			return out, nil
		}
	}
	if raw.Apply != nil {
		ops.Apply = func(ctx context.Context, request contract.ViewDesignRequest) contract.ViewDesignOutcome {
			if err := s.schemaDesignTarget(request.Original.Target); err != nil {
				return contract.ViewDesignOutcome{State: "not_executed", Error: err}
			}
			if !s.Capabilities().DesignViews {
				return contract.ViewDesignOutcome{State: "not_executed", Error: contract.ErrUnsupported}
			}
			r := request.Clone()
			r.Original.Target.Generation = s.raw.Generation()
			out := raw.Apply(ctx, r)
			if out.Actual != nil {
				actual := out.Actual.Clone()
				expected := r.Original.Target
				if r.Action != "drop" {
					expected.Object.Name = r.Desired.Name
				}
				if actual.Target != expected || actual.Schema.Exists != (r.Action != "drop") {
					out.Verified, out.Actual = false, nil
					out.Error = errors.Join(out.Error, errors.New("view receipt differs from captured target or existence"))
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
func (m *Manager) ViewDesign(id int64) contract.ViewDesignOperations {
	s, err := m.Session(id)
	if err != nil {
		return contract.ViewDesignOperations{}
	}
	p, err := contract.Require[contract.ViewDesignProvider](s)
	if err != nil {
		return contract.ViewDesignOperations{}
	}
	return p.ViewDesign()
}

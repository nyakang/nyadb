package contract

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/schemaddl"
)

type ViewDesignSnapshot struct {
	Target SchemaDesignTarget
	Schema schemaddl.ViewSnapshot
}

func (s ViewDesignSnapshot) Clone() ViewDesignSnapshot { s.Schema = s.Schema.Clone(); return s }

type ViewDesignRequest struct {
	Original ViewDesignSnapshot
	Action   string
	Desired  schemaddl.View
}

func (r ViewDesignRequest) Clone() ViewDesignRequest {
	r.Original = r.Original.Clone()
	r.Desired = r.Desired.Clone()
	return r
}

type ViewDesignOutcome struct {
	State    string
	Verified bool
	Actual   *ViewDesignSnapshot
	Error    error
}
type ViewDesignOperations struct {
	CanApply func(SchemaDesignTarget) bool
	Snapshot func(context.Context, SchemaDesignTarget) (ViewDesignSnapshot, error)
	Apply    func(context.Context, ViewDesignRequest) ViewDesignOutcome
}
type ViewDesignProvider interface{ ViewDesign() ViewDesignOperations }

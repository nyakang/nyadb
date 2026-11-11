package contract

import (
	"bytes"
	"slices"
)

// DataMutationPlan retains a captured original-document request and its native
// observations. Snapshot returns owned values, never submission authorization.
// Submit accepts only the owning adapter's original one-attempt handle. Close
// releases an unused preview; it does not undo or replay a dispatched mutation.
type DataMutationPlan interface {
	Snapshot() DataMutationSnapshot
	Close() error
}

type DataMutationItem struct {
	Kind, EntityID        string
	AffectedRelationships int
	Blocked               bool
}

// Optional immutable text keys for the shared confirmation. Nil on DataRuntime
// preserves the graph presentation. Adapters supply text; the UI localizes it.
type DataMutationPresentation struct {
	Title, Hint, PreviewLabel, HistoryLabel          string
	CreateLabel, UpdateLabel, DeleteLabel, CopyLabel string
	ShowRelationships                                bool
}

type DataMutationSnapshot struct {
	Object     ObjectRef
	Edits      []DataEdit
	Items      []DataMutationItem
	NativeJSON string // Complete bounded original observations and native parameters.
	// KeyPrefixDelete binds the original complete candidate-set operation.
	KeyPrefixDelete      *DataKeyPrefixDelete
	KeyPrefixDeleteBatch *DataKeyPrefixDeleteBatch
}

func CloneDataMutationSnapshot(s DataMutationSnapshot) DataMutationSnapshot {
	if s.KeyPrefixDeleteBatch != nil {
		copy := *s.KeyPrefixDeleteBatch
		s.KeyPrefixDeleteBatch = &copy
	}
	if s.KeyPrefixDelete != nil {
		copy := *s.KeyPrefixDelete
		s.KeyPrefixDelete = &copy
	}
	s.Items = slices.Clone(s.Items)
	s.Edits = slices.Clone(s.Edits)
	for i, e := range s.Edits {
		s.Edits[i].GraphEndpoints = CloneDataGraphEndpoints(e.GraphEndpoints)
		values := make(map[string]DataValue, len(e.Values))
		for k, v := range e.Values {
			v.Bytes = bytes.Clone(v.Bytes)
			values[k] = v
		}
		s.Edits[i].Values = values
		if e.Original == nil {
			continue
		}
		r := *e.Original
		r.Ref.Columns = slices.Clone(r.Ref.Columns)
		r.Ref.Values = slices.Clone(r.Ref.Values)
		for j := range r.Ref.Values {
			r.Ref.Values[j] = bytes.Clone(r.Ref.Values[j])
		}
		r.Ref.TypedValues = slices.Clone(r.Ref.TypedValues)
		for j := range r.Ref.TypedValues {
			r.Ref.TypedValues[j] = r.Ref.TypedValues[j].Clone()
		}
		r.Columns = slices.Clone(r.Columns)
		r.Original = slices.Clone(r.Original)
		for j := range r.Original {
			r.Original[j] = r.Original[j].Clone()
		}
		r.Fingerprints = slices.Clone(r.Fingerprints)
		for j, f := range r.Fingerprints {
			if f != nil {
				copy := *f
				r.Fingerprints[j] = &copy
			}
		}
		s.Edits[i].Original = &r
	}
	return s
}

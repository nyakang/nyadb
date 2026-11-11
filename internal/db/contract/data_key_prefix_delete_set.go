package contract

import (
	"context"
	"slices"
)

// DataKeyPrefixDeleteSet owns one complete original prefix observation. Each
// batch needs its own preview and explicit submit. Earlier batches may commit
// before a later batch conflicts; this is not a transaction across the set.
type DataKeyPrefixDeleteSet interface {
	Snapshot() DataKeyPrefixDeleteSetSnapshot
	PreviewBatch(context.Context, int) (DataMutationPlan, error)
	Close() error
}

type DataKeyPrefixDeleteBatch struct {
	Request                     DataKeyPrefixDelete
	Index, Offset, Count, Total int
}

// NextBatch advances only after a complete corresponding original receipt.
// InFlight waits for executor acknowledgement. Uncertain prohibits all further
// batches; later observations cannot confirm an earlier transaction.
type DataKeyPrefixDeleteSetSnapshot struct {
	Request              DataKeyPrefixDelete
	Candidates           []*RowSnapshot
	NativeJSON           string
	BatchSize, NextBatch int
	InFlight, Uncertain  bool
	// Stopped ends this capture after a known unsuccessful batch. Earlier
	// receipts remain complete; further work needs a separate new capture.
	Stopped  bool
	Receipts []string
}

func CloneDataKeyPrefixDeleteSetSnapshot(s DataKeyPrefixDeleteSetSnapshot) DataKeyPrefixDeleteSetSnapshot {
	s.Candidates = slices.Clone(s.Candidates)
	for i, original := range s.Candidates {
		if original == nil {
			continue
		}
		copy := CloneDataMutationSnapshot(DataMutationSnapshot{Edits: []DataEdit{{Original: original}}})
		s.Candidates[i] = copy.Edits[0].Original
	}
	s.Receipts = slices.Clone(s.Receipts)
	return s
}

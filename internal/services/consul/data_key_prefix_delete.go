package consul

import (
	"context"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// validKeyPrefixDeletion limits the original visible namespace, without
// treating slash-separated segments as directories or allowing the empty root.
func (l *dataLease) validKeyPrefixDeletion(prefix string) bool {
	return prefix != "" && validKey(prefix, false) && strings.HasPrefix(prefix, string(l.session.scope.Prefix))
}

// prepareKeyPrefixDeletion consumes a complete original visible-prefix response.
// It prepares only the captured finite key set: newly created keys are never
// inferred as candidates, and there is no native range-absence/MVCC guarantee.
// The owning preview must retain the prefix and complete original response in
// its immutable evidence before any plan is published.
func (l *dataLease) prepareKeyPrefixDeletion(prefix string, result contract.KVResult) ([]preparedDataEdit, error) {
	if !l.validKeyPrefixDeletion(prefix) {
		return nil, ErrInvalid
	}
	entries, err := l.entries(result)
	if err != nil {
		return nil, err
	}
	if result.HTTP.Metadata.ACLFiltered != nil && *result.HTTP.Metadata.ACLFiltered {
		return nil, contract.ErrUnsupported
	}
	// Reject the real operation bound before building thousands of snapshots.
	if len(entries) > 64/3 {
		return nil, ErrBounds
	}
	if len(entries) == 0 {
		return nil, ErrInvalid
	}
	edits := make([]contract.DataEdit, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Key, prefix) {
			return nil, ErrProtocol
		}
		if entry.Session != "" {
			return nil, contract.ErrUnsupported
		}
		edits = append(edits, contract.DataEdit{Kind: "delete", Original: l.snapshot(entry, result.HTTP.Metadata)})
	}
	// Existing preparation accounts for the real check-index/delete-CAS/absence
	// operations and rejects oversized requests before submission or splitting.
	return l.prepareDataEdits(edits)
}

// Scope retains the complete original observation independently of the later
// native comparison receipt. It describes captured candidates, not an atomic
// promise about the entire live prefix at commit time.
// The extra retained complete prefix capture stays within the original fixed
// plan reservation; count/field/export keep their independent larger budgets.
const dataKeyPrefixObservationBudget = 8 << 20

type dataKeyPrefixDeletionScope struct {
	Prefix     string
	Candidates int
	Original   contract.KVHTTPResult
	Batch      *contract.DataKeyPrefixDeleteBatch `json:",omitempty"`
	set        *dataKeyPrefixDeletionSet
}

func (l *dataLease) previewKeyPrefixDelete(ctx context.Context, id int64, catalog, name string, request contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
	if request.Object != l.object || !l.validKeyPrefixDeletion(request.Prefix) {
		return nil, ErrInvalid
	}
	scope := &dataKeyPrefixDeletionScope{Prefix: request.Prefix}
	return l.previewPreparedScope(ctx, id, catalog, name, scope, func(ctx context.Context) ([]preparedDataEdit, error) {
		native, err := KeyListRequest(request.Prefix, false)
		if err != nil {
			return nil, err
		}
		document, err := DocumentRequest(native)
		if err != nil {
			return nil, err
		}
		preparation, err := l.write.Prepare(document)
		if err != nil {
			return nil, err
		}
		result, err := l.write.Execute(ctx, preparation)
		if err != nil {
			return nil, err
		}
		prepared, err := l.prepareKeyPrefixDeletion(request.Prefix, result)
		if err != nil {
			return nil, err
		}
		retained := len(result.HTTP.Body) + len(result.HTTP.Request.Body)
		// Prepared originals hold signed hashes; include every decoded value in
		// the extra original response independently of the native JSON body.
		entries, err := l.entries(result)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			retained += len(entry.Value)
		}
		if retained > dataKeyPrefixObservationBudget {
			return nil, ErrBounds
		}
		scope.Original = result.HTTP.Clone()
		scope.Candidates = len(prepared)
		return prepared, nil
	})
}

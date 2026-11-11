package consul

import (
	"context"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// Conditional copy shares the original one-attempt plan/executor while
// remaining separate from ordinary insert/update edits.
func (l *dataLease) previewKeyCopy(ctx context.Context, id int64, catalog, name string, request contract.DataKeyCopy) (contract.DataMutationPlan, error) {
	return l.previewPrepared(ctx, id, catalog, name, func() ([]preparedDataEdit, error) {
		return l.prepareKeyCopy(request)
	})
}

func (l *dataLease) prepareKeyCopy(request contract.DataKeyCopy) ([]preparedDataEdit, error) {
	o, err := l.validateSnapshot(request.Source)
	if err != nil {
		return nil, err
	}
	before, err := observationEntry(o)
	if err != nil {
		return nil, err
	}
	if o.Session != "" {
		return nil, contract.ErrUnsupported
	}
	if !validKey(request.Destination, false) || request.Destination == o.Key ||
		!strings.HasPrefix(request.Destination, string(l.session.scope.Prefix)) {
		return nil, ErrInvalid
	}
	request = contract.CloneDataKeyCopy(request)
	p := preparedDataEdit{copy: true, before: before, observation: o,
		after: contract.KVHTTPEntry{Key: request.Destination, Flags: before.Flags},
		edit: contract.DataEdit{Kind: "copy", Original: request.Source,
			Values: map[string]contract.DataValue{"key": {Kind: contract.Text, Text: request.Destination}}}}
	prepared := []preparedDataEdit{p}
	if _, _, err := mutationRequest(prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

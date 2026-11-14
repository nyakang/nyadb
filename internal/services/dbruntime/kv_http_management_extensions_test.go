package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"testing"
)

type partialHTTPManagement struct{ contract.KVRequestLease }

func (partialHTTPManagement) HTTPManagementOperations() []contract.KVHTTPManagementOperation {
	return nil
}
func (partialHTTPManagement) PrepareHTTPManagement(contract.KVHTTPManagementRequest) (contract.KVHTTPManagementPreparation, error) {
	return nil, contract.ErrUnsupported
}
func (partialHTTPManagement) ExecuteHTTPManagement(context.Context, contract.KVHTTPManagementPreparation) (contract.KVHTTPManagementResult, error) {
	return contract.KVHTTPManagementResult{}, contract.ErrUnsupported
}
func (partialHTTPManagement) LastHTTPManagement() contract.KVHTTPManagementResult {
	return contract.KVHTTPManagementResult{}
}

type partialHTTPManagementRestore struct {
	partialHTTPManagement
	contract.KVHTTPArchiveRestoring
}
type partialHTTPManagementEnterprise struct {
	partialHTTPManagement
	contract.KVHTTPEnterpriseDeletionPlanning
}
type partialHTTPManagementBoth struct {
	partialHTTPManagement
	contract.KVHTTPArchiveRestoring
	contract.KVHTTPEnterpriseDeletionPlanning
}

func TestHTTPManagementBindingPreservesOriginalOptionalRestoreAndEnterpriseMethodSets(t *testing.T) {
	for _, tc := range []struct {
		raw                 contract.KVRequestLease
		restore, enterprise bool
	}{
		{partialHTTPManagement{}, false, false}, {partialHTTPManagementRestore{}, true, false}, {partialHTTPManagementEnterprise{}, false, true}, {partialHTTPManagementBoth{}, true, true},
	} {
		base := &kvRequestLease{raw: tc.raw}
		bound := bindKVHTTPManagement(base)
		_, restore := bound.(contract.KVHTTPArchiveRestoring)
		_, enterprise := bound.(contract.KVHTTPEnterpriseDeletionPlanning)
		if restore != tc.restore || enterprise != tc.enterprise || originalKVRequestLease(bound) != base {
			t.Fatal("binding changed original optional features")
		}
	}
}

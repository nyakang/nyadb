package dbruntime

import (
	"bytes"
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func (s *session) captureDataKVKey(r contract.DataRuntime) func(context.Context, int64, string, string, contract.KVKeyReadRequest) (*contract.KVKeyRead, error) {
	if r.CaptureKVKeyRead == nil {
		return nil
	}
	return func(ctx context.Context, id int64, db, table string, input contract.KVKeyReadRequest) (*contract.KVKeyRead, error) {
		if err := s.require(r.Capabilities.KeyValues, id); err != nil {
			return nil, err
		}
		request := input.Clone()
		if request.Snapshot == nil || request.Cursor == nil {
			return nil, connections.ErrInvalid
		}
		if err := s.dataObject(id, db, table, request.Snapshot.Ref.Object); err != nil {
			return nil, err
		}
		result, err := r.CaptureKVKeyRead(ctx, id, s.dataDatabase(db), table, request.Clone())
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = s.require(r.Capabilities.KeyValues, id); err != nil {
			return nil, err
		}
		expected := s.KVScope()
		ref := request.Snapshot.Ref
		if result == nil || result.Revision <= 0 || len(ref.Values) != 1 || len(result.Key) == 0 || len(result.Key) > 4096 || !bytes.Equal(result.Key, ref.Values[0]) || result.Scope.Namespace != expected.Namespace || result.Scope.Generation != s.raw.Generation() || !bytes.Equal(result.Scope.Prefix, expected.Prefix) || result.Scope.Header != expected.Header || result.Scope.Version != expected.Version {
			return nil, connections.ErrInvalid
		}
		captured := result.Clone()
		captured.Scope.Generation = s.generation
		return &captured, nil
	}
}

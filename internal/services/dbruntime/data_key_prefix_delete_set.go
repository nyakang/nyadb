package dbruntime

import (
	"context"
	"reflect"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type dataKeyPrefixDeleteSet struct {
	session *session
	request contract.DataKeyPrefixDelete
	initial contract.DataKeyPrefixDeleteSetSnapshot
	raw     contract.DataKeyPrefixDeleteSet
}

func validPrefixDeleteSetSnapshot(request contract.DataKeyPrefixDelete, snapshot contract.DataKeyPrefixDeleteSetSnapshot) bool {
	if snapshot.Request != request || snapshot.BatchSize < 1 || snapshot.NativeJSON == "" || len(snapshot.Candidates) == 0 {
		return false
	}
	if snapshot.NextBatch < 0 || snapshot.NextBatch > (len(snapshot.Candidates)+snapshot.BatchSize-1)/snapshot.BatchSize {
		return false
	}
	seen := map[string]bool{}
	for _, candidate := range snapshot.Candidates {
		if candidate == nil || candidate.Ref.Object != request.Object || len(candidate.Ref.TypedValues) != 1 {
			return false
		}
		key := candidate.Ref.TypedValues[0]
		if key.Kind != contract.Text || key.Truncated || key.Text == "" || !strings.HasPrefix(key.Text, request.Prefix) || seen[key.Text] {
			return false
		}
		seen[key.Text] = true
	}
	return true
}

func (s *dataKeyPrefixDeleteSet) Snapshot() contract.DataKeyPrefixDeleteSetSnapshot {
	if s.session.require(true, s.request.Object.ProfileID) != nil {
		return contract.DataKeyPrefixDeleteSetSnapshot{}
	}
	snapshot := s.raw.Snapshot()
	if !validPrefixDeleteSetSnapshot(s.request, snapshot) || snapshot.NativeJSON != s.initial.NativeJSON || snapshot.BatchSize != s.initial.BatchSize || !reflect.DeepEqual(snapshot.Candidates, s.initial.Candidates) {
		return contract.DataKeyPrefixDeleteSetSnapshot{}
	}
	return contract.CloneDataKeyPrefixDeleteSetSnapshot(snapshot)
}

func (s *dataKeyPrefixDeleteSet) Close() error { return s.raw.Close() }

func (s *dataKeyPrefixDeleteSet) PreviewBatch(ctx context.Context, index int) (contract.DataMutationPlan, error) {
	if err := s.session.require(true, s.request.Object.ProfileID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	initial := s.initial
	current := s.Snapshot()
	if len(current.Candidates) == 0 {
		return nil, contract.ErrRevoked
	}
	if current.Uncertain {
		return nil, contract.ErrDataCommitUncertain
	}
	if current.InFlight {
		return nil, contract.ErrKVBusy
	}
	if current.Stopped {
		return nil, contract.ErrDataConflict
	}
	if index != current.NextBatch {
		return nil, connections.ErrInvalid
	}
	if index < 0 || index >= (len(initial.Candidates)+initial.BatchSize-1)/initial.BatchSize {
		return nil, connections.ErrInvalid
	}
	plan, err := s.raw.PreviewBatch(ctx, index)
	if err == nil {
		err = s.session.require(true, s.request.Object.ProfileID)
	}
	if err == nil && plan == nil {
		err = contract.ErrUnsupported
	}
	if err == nil {
		snapshot := plan.Snapshot()
		start := index * initial.BatchSize
		end := min(start+initial.BatchSize, len(initial.Candidates))
		batch := contract.DataKeyPrefixDeleteBatch{Request: s.request, Index: index, Offset: start, Count: end - start, Total: len(initial.Candidates)}
		if snapshot.Object != s.request.Object || snapshot.KeyPrefixDelete == nil || *snapshot.KeyPrefixDelete != s.request || snapshot.KeyPrefixDeleteBatch == nil || *snapshot.KeyPrefixDeleteBatch != batch || len(snapshot.Edits) != batch.Count {
			err = connections.ErrInvalid
		} else {
			for i, edit := range snapshot.Edits {
				if edit.Kind != "delete" || len(edit.Values) != 0 || edit.GraphEndpoints != nil || !reflect.DeepEqual(edit.Original, initial.Candidates[start+i]) {
					err = connections.ErrInvalid
					break
				}
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = s.session.require(true, s.request.Object.ProfileID)
	}
	if err != nil {
		if plan != nil {
			plan.Close()
		}
		return nil, err
	}
	return &dataMutationPlan{session: s.session, object: s.request.Object, raw: plan}, nil
}

func mSessionDataKeyPrefixDeleteSet(s *session, r, out contract.DataRuntime) contract.DataRuntime {
	if r.CaptureKeyPrefixDelete == nil {
		return out
	}
	out.CaptureKeyPrefixDelete = func(ctx context.Context, id int64, db, table string, request contract.DataKeyPrefixDelete) (contract.DataKeyPrefixDeleteSet, error) {
		if err := s.require(r.Capabilities.EditableTables && r.Capabilities.KeyValues, id); err != nil {
			return nil, err
		}
		if request.Object.ProfileID != id {
			return nil, connections.ErrInvalid
		}
		if err := s.dataObject(id, db, table, request.Object); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := r.CaptureKeyPrefixDelete(ctx, id, s.dataDatabase(db), table, request)
		if err == nil {
			err = s.require(r.Capabilities.EditableTables && r.Capabilities.KeyValues, id)
		}
		if err == nil && raw == nil {
			err = contract.ErrUnsupported
		}
		var snapshot contract.DataKeyPrefixDeleteSetSnapshot
		if err == nil {
			snapshot = raw.Snapshot()
			if !validPrefixDeleteSetSnapshot(request, snapshot) || snapshot.NextBatch != 0 || snapshot.InFlight || snapshot.Uncertain || snapshot.Stopped || len(snapshot.Receipts) != 0 {
				err = connections.ErrInvalid
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = s.require(r.Capabilities.EditableTables && r.Capabilities.KeyValues, id)
		}
		if err != nil {
			if raw != nil {
				raw.Close()
			}
			return nil, err
		}
		return &dataKeyPrefixDeleteSet{session: s, request: request, initial: contract.CloneDataKeyPrefixDeleteSetSnapshot(snapshot), raw: raw}, nil
	}
	return out
}

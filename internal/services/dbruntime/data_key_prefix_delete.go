package dbruntime

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

func mSessionDataKeyPrefixDelete(s *session, r, out contract.DataRuntime) contract.DataRuntime {
	if r.PreviewKeyPrefixDelete == nil {
		return out
	}
	out.PreviewKeyPrefixDelete = func(ctx context.Context, id int64, db, table string, request contract.DataKeyPrefixDelete) (contract.DataMutationPlan, error) {
		if err := s.require(r.Capabilities.EditableTables && r.Capabilities.KeyValues, id); err != nil {
			return nil, err
		}
		object := request.Object
		if object.ProfileID != id {
			return nil, connections.ErrInvalid
		}
		if err := s.dataObject(id, db, table, object); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		plan, err := r.PreviewKeyPrefixDelete(ctx, id, s.dataDatabase(db), table, request)
		if err == nil {
			err = s.require(r.Capabilities.EditableTables && r.Capabilities.KeyValues, id)
		}
		if err == nil && plan == nil {
			err = contract.ErrUnsupported
		}
		if err == nil && plan.Snapshot().Object != object {
			err = connections.ErrInvalid
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = s.require(r.Capabilities.EditableTables && r.Capabilities.KeyValues, id)
		}
		if err != nil {
			if plan != nil {
				_ = plan.Close()
			}
			return nil, err
		}
		return &dataMutationPlan{session: s, object: object, raw: plan}, nil
	}
	return out
}

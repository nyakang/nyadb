package dbruntime

import (
	"context"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

type graphImportPlan struct {
	session *session
	object  contract.ObjectRef
	raw     contract.GraphImportPlan
}

func (p *graphImportPlan) Snapshot() contract.GraphImportSnapshot {
	return contract.CloneGraphImportSnapshot(p.raw.Snapshot())
}
func (p *graphImportPlan) Close() error { return p.raw.Close() }

func mSessionGraphData(s *session, r contract.DataRuntime) contract.DataRuntime {
	out := r
	if r.PreviewGraphImport != nil {
		out.PreviewGraphImport = func(ctx context.Context, id int64, db, name string, request contract.GraphImportRequest) (contract.GraphImportPlan, error) {
			if err := s.require(r.Capabilities.Import, id); err != nil {
				return nil, err
			}
			p, err := r.PreviewGraphImport(ctx, id, s.dataDatabase(db), name, contract.CloneGraphImportRequest(request))
			if err == nil {
				err = s.require(r.Capabilities.Import, id)
			}
			if err == nil {
				err = ctx.Err()
			}
			var object contract.ObjectRef
			if err == nil {
				if p == nil {
					err = contract.ErrUnsupported
				} else {
					object = p.Snapshot().Object
					if object.ProfileID != id {
						err = connections.ErrInvalid
					} else {
						err = s.dataObject(id, db, name, object)
					}
				}
			}
			if err == nil {
				err = s.require(r.Capabilities.Import, id)
			}
			if err != nil {
				if p != nil {
					_ = p.Close()
				}
				return nil, err
			}
			return &graphImportPlan{session: s, object: object, raw: p}, nil
		}
	}
	if r.SubmitGraphImport != nil {
		out.SubmitGraphImport = func(ctx context.Context, id int64, db, name string, p contract.GraphImportPlan) (*contract.GraphImportResult, error) {
			if err := s.require(r.Capabilities.Import, id); err != nil {
				return nil, err
			}
			bound, ok := p.(*graphImportPlan)
			if !ok || bound == nil || bound.session != s {
				return nil, connections.ErrInvalid
			}
			if err := s.dataObject(id, db, name, bound.object); err != nil {
				return nil, err
			}
			result, err := r.SubmitGraphImport(ctx, id, s.dataDatabase(db), name, bound.raw)
			return contract.CloneGraphImportResult(result), err
		}
	}
	if r.ExportGraph != nil {
		out.ExportGraph = func(ctx context.Context, object contract.ObjectRef) (contract.GraphImportBundle, error) {
			if err := s.require(r.Capabilities.Export, object.ProfileID); err != nil {
				return contract.GraphImportBundle{}, err
			}
			if object.ProfileID == 0 {
				return contract.GraphImportBundle{}, connections.ErrInvalid
			}
			if err := s.dataObject(object.ProfileID, object.Catalog, object.Name, object); err != nil {
				return contract.GraphImportBundle{}, err
			}
			bundle, err := r.ExportGraph(ctx, object)
			if err == nil {
				err = s.require(r.Capabilities.Export, object.ProfileID)
			}
			if err == nil {
				err = ctx.Err()
			}
			if err == nil && bundle.Source != object.Namespace() {
				err = connections.ErrInvalid
			}
			if err != nil {
				return contract.GraphImportBundle{}, err
			}
			return contract.CloneGraphImportBundle(bundle), nil
		}
	}
	return out
}

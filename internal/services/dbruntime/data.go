package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"io"
	"slices"
)

func mSessionData(s *session, r contract.DataRuntime) contract.DataRuntime {
	out := mSessionGraphData(s, r)
	if r.ReserveDocument != nil {
		out.ReserveDocument = func(o contract.ObjectRef) (contract.DataDocumentLease, error) {
			if e := s.require(true, o.ProfileID); e != nil {
				return nil, e
			}
			l, e := r.ReserveDocument(o)
			if e != nil {
				if l != nil {
					_ = l.Close()
				}
				return nil, e
			}
			if l == nil {
				return nil, contract.ErrUnsupported
			}
			return &dataDocumentLease{session: s, raw: l}, nil
		}
	}
	out.DocumentImportModes = cloneColumns(r.DocumentImportModes)
	out.EditKinds = cloneColumns(r.EditKinds)
	out.PreviewEditKinds = cloneColumns(r.PreviewEditKinds)
	if r.MutationPresentation != nil {
		p := *r.MutationPresentation
		out.MutationPresentation = &p
	}
	out.EditValueKinds = slices.Clone(r.EditValueKinds)
	out.ExportFormats = append([]string(nil), r.ExportFormats...)
	out.FilterColumns = cloneColumns(r.FilterColumns)
	out.SortColumns = cloneColumns(r.SortColumns)
	out.Transactions = sessionTransactions(s, r.Transactions)
	if r.Loader != nil {
		out.Loader = func(ctx context.Context, id int64, db, table string, offset int64, limit int) (*contract.DataPage, error) {
			if e := s.require(true, id); e != nil {
				return nil, e
			}
			return r.Loader(ctx, id, s.dataDatabase(db), table, offset, limit)
		}
	}
	if r.AdvancedLoader != nil {
		out.AdvancedLoader = func(ctx context.Context, id int64, db, table string, options contract.DataOptions) (*contract.DataPage, error) {
			if e := s.require(true, id); e != nil {
				return nil, e
			}
			if e := s.dataObject(id, db, table, options.Object); e != nil {
				return nil, e
			}
			return r.AdvancedLoader(ctx, id, s.dataDatabase(db), table, options)
		}
	}
	if r.Saver != nil {
		out.Saver = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
			if e := s.require(r.Capabilities.EditableTables, id); e != nil {
				return nil, e
			}
			for _, edit := range edits {
				if edit.GraphEndpoints != nil {
					return nil, contract.ErrUnsupported
				}
				if edit.Original != nil {
					if e := s.dataObject(id, db, table, edit.Original.Ref.Object); e != nil {
						return nil, e
					}
				}
			}
			return r.Saver(ctx, id, s.dataDatabase(db), table, edits)
		}
	}
	if r.PreviewEdits != nil {
		out.PreviewEdits = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (contract.DataMutationPlan, error) {
			if e := s.require(r.Capabilities.EditableTables, id); e != nil {
				return nil, e
			}
			captured := contract.CloneDataMutationSnapshot(contract.DataMutationSnapshot{Edits: edits}).Edits
			for _, edit := range captured {
				if endpoint := edit.GraphEndpoints; endpoint != nil {
					if r.ReadGraphEndpoints == nil || edit.Kind != "insert" {
						return nil, contract.ErrUnsupported
					}
					if endpoint.Object.ProfileID != id || endpoint.Object.Kind != "relationship" {
						return nil, connections.ErrInvalid
					}
					if err := s.dataObject(id, db, table, endpoint.Object); err != nil {
						return nil, err
					}
				}
				if edit.Original != nil {
					if e := s.dataObject(id, db, table, edit.Original.Ref.Object); e != nil {
						return nil, e
					}
				}
			}
			plan, err := r.PreviewEdits(ctx, id, s.dataDatabase(db), table, captured)
			if err == nil {
				err = s.require(r.Capabilities.EditableTables, id)
			}
			if err == nil && plan == nil {
				err = contract.ErrUnsupported
			}
			var object contract.ObjectRef
			if err == nil {
				object = plan.Snapshot().Object
				if object.ProfileID != id {
					err = connections.ErrInvalid
				} else {
					err = s.dataObject(id, db, table, object)
				}
			}
			if err == nil {
				err = s.require(r.Capabilities.EditableTables, id)
			}
			if err != nil {
				if plan != nil {
					_ = plan.Close()
				}
				return nil, err
			}
			return &dataMutationPlan{session: s, object: object, raw: plan}, nil
		}
	}
	if r.ReadGraphEndpoints != nil {
		out.ReadGraphEndpoints = func(ctx context.Context, id int64, db, table, source, target string) (*contract.DataGraphEndpoints, error) {
			if err := s.require(r.Capabilities.EditableTables, id); err != nil {
				return nil, err
			}
			endpoints, err := r.ReadGraphEndpoints(ctx, id, s.dataDatabase(db), table, source, target)
			if err != nil {
				return nil, err
			}
			if err := s.require(r.Capabilities.EditableTables, id); err != nil {
				return nil, err
			}
			if endpoints == nil || endpoints.Object.ProfileID != id || endpoints.Object.Kind != "relationship" || endpoints.Source.ElementID != source || endpoints.Target.ElementID != target {
				return nil, connections.ErrInvalid
			}
			if err := s.dataObject(id, db, table, endpoints.Object); err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := s.require(r.Capabilities.EditableTables, id); err != nil {
				return nil, err
			}
			return contract.CloneDataGraphEndpoints(endpoints), nil
		}
	}
	if r.SubmitEdits != nil {
		out.SubmitEdits = func(ctx context.Context, id int64, db, table string, plan contract.DataMutationPlan) (*contract.DataEditResult, error) {
			if e := s.require(r.Capabilities.EditableTables, id); e != nil {
				return nil, e
			}
			bound, ok := plan.(*dataMutationPlan)
			if !ok || bound == nil || bound.session != s {
				return nil, connections.ErrInvalid
			}
			if err := s.dataObject(id, db, table, bound.object); err != nil {
				return nil, err
			}
			return r.SubmitEdits(ctx, id, s.dataDatabase(db), table, bound.raw)
		}
	}
	if r.RowReader != nil {
		out.RowReader = func(ctx context.Context, id int64, db, table string, ref contract.RowRef) (*contract.DataRow, error) {
			if e := s.require(true, id); e != nil {
				return nil, e
			}
			if e := s.dataObject(id, db, table, ref.Object); e != nil {
				return nil, e
			}
			return r.RowReader(ctx, id, s.dataDatabase(db), table, ref)
		}
	}
	if r.Verify != nil {
		out.Verify = func(ctx context.Context, id int64, db, table string, result *contract.DataEditResult) (*contract.DataVerification, error) {
			if e := s.require(true, id); e != nil {
				return nil, e
			}
			return r.Verify(ctx, id, s.dataDatabase(db), table, result)
		}
	}
	if r.Exporter != nil {
		out.Exporter = func(ctx context.Context, id int64, db, table string, options contract.DataOptions, columns []string, sink func([]contract.DataValue) error) error {
			if e := s.require(r.Capabilities.Export, id); e != nil {
				return e
			}
			if e := s.dataObject(id, db, table, options.Object); e != nil {
				return e
			}
			return r.Exporter(ctx, id, s.dataDatabase(db), table, options, columns, sink)
		}
	}
	if r.Analyzer != nil {
		out.Analyzer = func(ctx context.Context, id int64, db, table string, options contract.DataOptions, columns []string) (*contract.DataAnalysis, error) {
			if e := s.require(r.Capabilities.Analysis, id); e != nil {
				return nil, e
			}
			if e := s.dataObject(id, db, table, options.Object); e != nil {
				return nil, e
			}
			return r.Analyzer(ctx, id, s.dataDatabase(db), table, options, columns)
		}
	}
	if r.NewTableImporter != nil {
		out.NewTableImporter = func(ctx context.Context, id int64, db, table string, columns []contract.DataImportColumn, edits []contract.DataEdit) (*contract.DataNewTableImportResult, error) {
			if e := s.require(r.Capabilities.Import, id); e != nil {
				return nil, e
			}
			return r.NewTableImporter(ctx, id, s.dataDatabase(db), table, columns, edits)
		}
	}
	if r.Importer != nil {
		out.Importer = func(ctx context.Context, id int64, db, table string, req contract.DataImportRequest) (*contract.DataImportResult, error) {
			if e := s.require(r.Capabilities.Import, id); e != nil {
				return nil, e
			}
			return r.Importer(ctx, id, s.dataDatabase(db), table, req)
		}
	}
	if r.StreamImporter != nil {
		out.StreamImporter = func(ctx context.Context, id int64, db, table string, req contract.DataImportStreamRequest) (*contract.DataImportStreamResult, error) {
			if e := s.require(r.Capabilities.Import, id); e != nil {
				return nil, e
			}
			if req.Generation != 0 && req.Generation != s.generation {
				return nil, contract.ErrRevoked
			}
			req.Generation = s.raw.Generation()
			return r.StreamImporter(ctx, id, s.dataDatabase(db), table, req)
		}
	}
	if r.TableMetadata != nil {
		out.TableMetadata = func(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
			if e := s.require(r.Capabilities.Metadata, id); e != nil {
				return nil, e
			}
			nativeDB := s.dataDatabase(db)
			value, err := r.TableMetadata(ctx, id, nativeDB, table)
			if value != nil && s.kind == connections.Solr && value.Database == nativeDB && value.Name == table {
				copy := *value
				copy.Database = db
				value = &copy
			}
			return value, err
		}
	}
	if r.TableInfo != nil {
		out.TableInfo = func(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
			if e := s.require(r.Capabilities.Metadata, id); e != nil {
				return nil, e
			}
			nativeDB := s.dataDatabase(db)
			value, err := r.TableInfo(ctx, id, nativeDB, table)
			if value != nil && s.kind == connections.Solr && value.Database == nativeDB && value.Name == table {
				copy := *value
				copy.Database = db
				value = &copy
			}
			return value, err
		}
	}
	if r.Count != nil {
		out.Count = func(ctx context.Context, id int64, db, table string) (int64, error) {
			if e := s.require(r.Capabilities.Count, id); e != nil {
				return 0, e
			}
			return r.Count(ctx, id, s.dataDatabase(db), table)
		}
	}
	if r.Chunk != nil {
		out.Chunk = func(ctx context.Context, id int64, db, table, column string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
			if e := s.require(r.Capabilities.FieldChunks, id); e != nil {
				return nil, e
			}
			if e := s.dataObject(id, db, table, ref.Object); e != nil {
				return nil, e
			}
			return r.Chunk(ctx, id, s.dataDatabase(db), table, column, ref, offset, units)
		}
	}
	if r.JSONL != nil {
		out.JSONL = func(ctx context.Context, object contract.ObjectRef, w io.Writer) error {
			if e := s.require(r.Capabilities.JSONL, object.ProfileID); e != nil {
				return e
			}
			return r.JSONL(ctx, object, w)
		}
	}
	return out
}

type dataDocumentLease struct {
	session *session
	raw     contract.DataDocumentLease
}

type dataMutationPlan struct {
	session *session
	object  contract.ObjectRef
	raw     contract.DataMutationPlan
}

func (p *dataMutationPlan) Snapshot() contract.DataMutationSnapshot {
	return contract.CloneDataMutationSnapshot(p.raw.Snapshot())
}
func (p *dataMutationPlan) Close() error { return p.raw.Close() }

// Workbench labels resolve through the owning session. Solr's native data
// leases retain the observed endpoint catalog as their immutable identity.
func (s *session) dataDatabase(label string) string {
	if s.kind == connections.Solr {
		return s.Namespace(label).Catalog
	}
	return label
}

func (l *dataDocumentLease) Runtime() contract.DataRuntime {
	r := l.raw.Runtime()
	r.ReserveDocument = nil
	r.Capabilities = contract.IntersectCapabilities(r.Capabilities, l.session.Capabilities())
	r.Capabilities = contract.BoundDataCapabilities(r)
	return mSessionData(l.session, r)
}
func (l *dataDocumentLease) Close() error { return l.raw.Close() }

func cloneColumns(v []string) []string {
	if v == nil {
		return nil
	}
	return append([]string{}, v...)
}

func (s *session) dataObject(id int64, db, table string, o contract.ObjectRef) error {
	if o.ProfileID == 0 {
		return nil
	}
	if o.ProfileID != id || o.Name != table || o.Namespace() != s.Namespace(db) {
		return connections.ErrInvalid
	}
	return nil
}

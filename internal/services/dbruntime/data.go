package dbruntime

import (
	"context"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"io"
)

func mSessionData(s *session, r contract.DataRuntime) contract.DataRuntime {
	out := r
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
	out.ExportFormats = append([]string(nil), r.ExportFormats...)
	out.Transactions = sessionTransactions(s, r.Transactions)
	if r.Loader != nil {
		out.Loader = func(ctx context.Context, id int64, db, table string, offset int64, limit int) (*contract.DataPage, error) {
			if e := s.require(true, id); e != nil {
				return nil, e
			}
			return r.Loader(ctx, id, db, table, offset, limit)
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
			return r.AdvancedLoader(ctx, id, db, table, options)
		}
	}
	if r.Saver != nil {
		out.Saver = func(ctx context.Context, id int64, db, table string, edits []contract.DataEdit) (*contract.DataEditResult, error) {
			if e := s.require(r.Capabilities.EditableTables, id); e != nil {
				return nil, e
			}
			for _, edit := range edits {
				if edit.Original != nil {
					if e := s.dataObject(id, db, table, edit.Original.Ref.Object); e != nil {
						return nil, e
					}
				}
			}
			return r.Saver(ctx, id, db, table, edits)
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
			return r.RowReader(ctx, id, db, table, ref)
		}
	}
	if r.Verify != nil {
		out.Verify = func(ctx context.Context, id int64, db, table string, result *contract.DataEditResult) (*contract.DataVerification, error) {
			if e := s.require(true, id); e != nil {
				return nil, e
			}
			return r.Verify(ctx, id, db, table, result)
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
			return r.Exporter(ctx, id, db, table, options, columns, sink)
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
			return r.Analyzer(ctx, id, db, table, options, columns)
		}
	}
	if r.NewTableImporter != nil {
		out.NewTableImporter = func(ctx context.Context, id int64, db, table string, columns []contract.DataImportColumn, edits []contract.DataEdit) (*contract.DataNewTableImportResult, error) {
			if e := s.require(r.Capabilities.Import, id); e != nil {
				return nil, e
			}
			return r.NewTableImporter(ctx, id, db, table, columns, edits)
		}
	}
	if r.Importer != nil {
		out.Importer = func(ctx context.Context, id int64, db, table string, req contract.DataImportRequest) (*contract.DataImportResult, error) {
			if e := s.require(r.Capabilities.Import, id); e != nil {
				return nil, e
			}
			return r.Importer(ctx, id, db, table, req)
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
			return r.StreamImporter(ctx, id, db, table, req)
		}
	}
	if r.TableMetadata != nil {
		out.TableMetadata = func(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
			if e := s.require(r.Capabilities.Metadata, id); e != nil {
				return nil, e
			}
			return r.TableMetadata(ctx, id, db, table)
		}
	}
	if r.TableInfo != nil {
		out.TableInfo = func(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
			if e := s.require(r.Capabilities.Metadata, id); e != nil {
				return nil, e
			}
			return r.TableInfo(ctx, id, db, table)
		}
	}
	if r.Count != nil {
		out.Count = func(ctx context.Context, id int64, db, table string) (int64, error) {
			if e := s.require(r.Capabilities.Count, id); e != nil {
				return 0, e
			}
			return r.Count(ctx, id, db, table)
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
			return r.Chunk(ctx, id, db, table, column, ref, offset, units)
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

func (l *dataDocumentLease) Runtime() contract.DataRuntime {
	r := l.raw.Runtime()
	r.ReserveDocument = nil
	r.Capabilities = contract.IntersectCapabilities(r.Capabilities, l.session.Capabilities())
	return mSessionData(l.session, r)
}
func (l *dataDocumentLease) Close() error { return l.raw.Close() }

func (s *session) dataObject(id int64, db, table string, o contract.ObjectRef) error {
	if o.ProfileID == 0 {
		return nil
	}
	if o.ProfileID != id || o.Name != table || o.Namespace() != s.Namespace(db) {
		return connections.ErrInvalid
	}
	return nil
}

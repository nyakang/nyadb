package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func (l *dataLease) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if err := l.identity(id, db, name); err != nil {
		return 0, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return 0, err
	}
	defer closeClient()
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	page, _, err := c.ListDocuments(ctx, db, DocumentsRequest{Limit: 0})
	if err != nil {
		return 0, err
	}
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	return int64(page.Total), ctx.Err()
}
func (l *dataLease) metadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return nil, err
	}
	defer closeClient()
	raw, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	return &contract.TableStructure{Object: l.object, Database: db, Name: name, Kind: "documents", Engine: "Meilisearch", Columns: documentColumns(), Definition: string(raw), Comment: "Native live document enumeration; no atomic index incarnation or snapshot guarantee"}, ctx.Err()
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	metadata, err := l.metadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: "documents", Engine: metadata.Engine, Comment: metadata.Comment}, nil
}
func (l *dataLease) stream(ctx context.Context, id int64, db, name string, o contract.DataOptions, sink func(dataDocument) error) error {
	if err := l.identity(id, db, name); err != nil {
		return err
	}
	if o.Object != l.object || o.Offset != 0 || o.After != nil || sink == nil {
		return contract.ErrUnsupported
	}
	if err := validateDataOptions(o); err != nil {
		return err
	}
	ctx, done, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	c, closeClient, err := l.client(ctx)
	if err != nil {
		return err
	}
	defer closeClient()
	if _, err = l.guard(ctx, c); err != nil {
		return err
	}
	initial, _, err := c.ListDocuments(ctx, db, DocumentsRequest{Limit: 0})
	if err != nil {
		return err
	}
	spool, err := newExportSpool()
	if err != nil {
		return err
	}
	defer spool.close()
	for offset := uint64(0); offset < initial.Total; {
		if err = ctx.Err(); err != nil {
			return err
		}
		page, _, err := c.ListDocuments(ctx, db, DocumentsRequest{Offset: offset, Limit: 256})
		if err != nil {
			return err
		}
		if page.Total != initial.Total || len(page.Documents) == 0 {
			return contract.ErrDataConflict
		}
		if _, err = l.guard(ctx, c); err != nil {
			return err
		}
		for _, raw := range page.Documents {
			doc, err := l.document(raw)
			if err != nil {
				return err
			}
			var compact bytes.Buffer
			if err = json.Compact(&compact, doc.JSON); err != nil {
				return ErrProtocol
			}
			if err = spool.add(doc.ID, compact.Bytes()); err != nil {
				return err
			}
		}
		offset += uint64(len(page.Documents))
	}
	final, _, err := c.ListDocuments(ctx, db, DocumentsRequest{Limit: 0})
	if err != nil {
		return err
	}
	if final.Total != initial.Total {
		return contract.ErrDataConflict
	}
	if _, err = l.guard(ctx, c); err != nil {
		return err
	}
	if err = spool.validate(ctx); err != nil {
		return err
	}
	return spool.emit(ctx, func(raw []byte) error {
		doc, err := l.document(raw)
		if err != nil {
			return err
		}
		return sink(doc)
	})
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return ErrInvalid
	}
	if len(fields) > 2 {
		return contract.ErrUnsupported
	}
	fields = slices.Clone(fields)
	if len(fields) == 0 {
		fields = []string{"id", "document"}
	}
	indices := make([]int, len(fields))
	seen := map[string]bool{}
	for i, field := range fields {
		indices[i] = slices.Index([]string{"id", "document"}, field)
		if indices[i] < 0 || seen[field] {
			return contract.ErrUnsupported
		}
		seen[field] = true
	}
	return l.stream(ctx, id, db, name, o, func(doc dataDocument) error {
		cells := documentValues(doc)
		values := make([]contract.DataValue, len(fields))
		for i, index := range indices {
			values[i] = contract.DataValue{Kind: cells[index].Kind, Text: cells[index].Text}
		}
		return sink(values)
	})
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object || w == nil {
		return ErrInvalid
	}
	return l.stream(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, func(doc dataDocument) error {
		raw := append(bytes.Clone(doc.JSON), '\n')
		n, err := w.Write(raw)
		if err == nil && n != len(raw) {
			return io.ErrShortWrite
		}
		return err
	})
}

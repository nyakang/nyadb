package couchdb

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
	raw, err := l.guard(ctx, c)
	if err != nil {
		return 0, err
	}
	var info struct {
		Count json.Number `json:"doc_count"`
	}
	if decodeJSON(raw, &info) != nil {
		return 0, ErrProtocol
	}
	n, err := info.Count.Int64()
	if err != nil || n < 0 {
		return 0, ErrProtocol
	}
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	return n, ctx.Err()
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
	return &contract.TableStructure{Object: l.object, Database: db, Name: name, Kind: "documents", Engine: "Apache CouchDB", Columns: documentColumns(), Definition: string(raw), Comment: "Native JSON documents; the unfiltered range includes design documents; Mango filters exclude them"}, ctx.Err()
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	metadata, err := l.metadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: "documents", Engine: metadata.Engine, Comment: metadata.Comment}, nil
}
func (l *dataLease) stream(ctx context.Context, id int64, db, name string, o contract.DataOptions, sink func(NativeDocument) error) error {
	if err := l.identity(id, db, name); err != nil {
		return err
	}
	if o.Object != l.object || o.Offset != 0 || o.After != nil || sink == nil {
		return contract.ErrUnsupported
	}
	o, err := cloneDataOptions(o)
	if err != nil {
		return err
	}
	q, err := documentDataQuery(o)
	if err != nil {
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
	position := dataBoundary{}
	// Native key progress is strictly ordered; a fixed digest window detects
	// bad Mango cursor loops without retaining growing lists of bookmarks.
	history := make([]string, 0, 1024)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		page, err := l.readIDs(ctx, c, q, position, 100)
		if err != nil {
			return err
		}
		pending := make([]NativeDocument, 0, len(page.Documents))
		pendingBytes := 0
		flush := func() error {
			if _, err := l.guard(ctx, c); err != nil {
				return err
			}
			for _, doc := range pending {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := sink(doc); err != nil {
					return err
				}
			}
			clear(pending)
			pending = pending[:0]
			pendingBytes = 0
			return nil
		}
		for _, identity := range page.Documents {
			doc, err := c.ReadDocument(ctx, db, identity)
			if err != nil {
				return err
			}
			if pendingBytes+len(doc.JSON) > responseLimit && len(pending) > 0 {
				if err = flush(); err != nil {
					return err
				}
			}
			pending = append(pending, doc)
			pendingBytes += len(doc.JSON)
		}
		if err = flush(); err != nil {
			return err
		}
		if !page.HasMore {
			return ctx.Err()
		}
		next := dataBoundary{Key: page.NextKey, Bookmark: page.Bookmark}
		raw, _ := json.Marshal(next)
		digest := fingerprint(raw)
		if slices.Contains(history, digest) {
			return ErrProtocol
		}
		if len(history) == 1024 {
			copy(history, history[1:])
			history = history[:1023]
		}
		history = append(history, digest)
		position = next
	}
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if sink == nil {
		return ErrInvalid
	}
	if len(fields) > 3 {
		return contract.ErrUnsupported
	}
	fields = slices.Clone(fields)
	if len(fields) == 0 {
		fields = []string{"_id", "_rev", "document"}
	}
	indices := make([]int, len(fields))
	seen := map[string]bool{}
	for i, field := range fields {
		indices[i] = slices.Index([]string{"_id", "_rev", "document"}, field)
		if indices[i] < 0 || seen[field] {
			return contract.ErrUnsupported
		}
		seen[field] = true
	}
	return l.stream(ctx, id, db, name, o, func(doc NativeDocument) error {
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
	return l.stream(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, func(doc NativeDocument) error {
		var buffer bytes.Buffer
		if err := json.Compact(&buffer, doc.JSON); err != nil {
			return ErrProtocol
		}
		buffer.WriteByte('\n')
		raw := buffer.Bytes()
		n, err := w.Write(raw)
		if err == nil && n != len(raw) {
			return io.ErrShortWrite
		}
		return err
	})
}

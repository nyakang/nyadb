package mongodb

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *Session) DataReads() contract.DataReads {
	return contract.DataReads{Loader: s.load, AdvancedLoader: s.page, RowReader: s.row, TableMetadata: s.tableMetadata, TableInfo: s.tableInfo, Count: s.count, Chunk: s.chunk}
}

func (s *Session) tableInfo(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	metadata, err := s.tableMetadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	kind := "COLLECTION"
	if metadata.Object.Kind == "view" {
		kind = "VIEW"
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: kind, Engine: "MongoDB", Comment: "BSON documents; indexes and validators appear in collection metadata"}, nil
}
func (s *Session) DataEdits() contract.DataEdits {
	return contract.DataEdits{Saver: s.save, Verify: s.verify}
}
func (s *Session) DataExports() contract.DataExports {
	return contract.DataExports{JSONL: s.jsonl, Exporter: s.exportRows}
}

func (s *Session) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if id != s.ProfileID() || sink == nil {
		return connections.ErrInvalid
	}
	object := o.Object
	if object.ProfileID == 0 {
		object = s.Namespace(db).Object("collection", name)
	}
	if err := s.object(object); err != nil || object.Catalog != db || object.Name != name {
		return connections.ErrInvalid
	}
	if len(fields) == 0 {
		fields = []string{"_id", documentField}
	}
	for _, field := range fields {
		if field != "_id" && field != documentField {
			return contract.ErrUnsupported
		}
	}
	o.Limit = 1
	filter, opts, err := s.findOptions(o)
	if err != nil {
		return err
	}
	opts.SetLimit(0)
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	cur, err := s.client.Database(db).Collection(name).Find(ctx, filter, opts)
	if err != nil {
		return safeError(err)
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		values := make([]contract.DataValue, len(fields))
		for i, field := range fields {
			raw := cur.Current
			if field == "_id" {
				raw, err = idDocument(raw)
				if err != nil {
					return contract.ErrUnsupported
				}
			}
			text, err := extendedJSON(raw)
			if err != nil {
				return err
			}
			values[i] = contract.DataValue{Kind: contract.JSON, Text: text}
		}
		if err = sink(values); err != nil {
			return err
		}
	}
	return safeError(cur.Err())
}
func (s *Session) DataFilterModes() []string { return []string{"equal", "not_equal"} }
func (s *Session) tableMetadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	if id != s.ProfileID() {
		return nil, connections.ErrInvalid
	}
	objects, err := s.Browse(ctx, s.Namespace(db))
	if err != nil {
		return nil, err
	}
	for _, o := range objects {
		if o.Name == name {
			return s.TableMetadata(ctx, o.Ref)
		}
	}
	return nil, errors.New("MongoDB collection no longer exists")
}
func (s *Session) load(ctx context.Context, id int64, db, name string, offset int64, limit int) (*contract.DataPage, error) {
	return s.page(ctx, id, db, name, contract.DataOptions{Offset: offset, Limit: limit, Object: s.Namespace(db).Object("collection", name)})
}
func (s *Session) findOptions(o contract.DataOptions) (bson.D, *options.FindOptionsBuilder, error) {
	if o.Offset < 0 || o.Limit < 1 || o.Limit > contract.DataMaxPageSize || o.UseKeyset || o.After != nil || len(o.Filters) > 0 || o.Condition != nil || len(o.Sorts) > 0 {
		return nil, nil, contract.ErrUnsupported
	}
	filter := bson.D{}
	if o.FilterColumn != "" {
		if o.FilterColumn != "_id" || o.FilterMode != "equal" && o.FilterMode != "not_equal" {
			return nil, nil, contract.ErrUnsupported
		}
		id, err := parseDocument(o.FilterValue, contract.DataEditMaxBytes)
		if err != nil {
			return nil, nil, err
		}
		if lenMustElements(id) != 1 || id.Lookup("_id").Type == 0 {
			return nil, nil, errors.New("MongoDB _id filter requires {\"_id\": <Extended JSON value>}")
		}
		value := any(id.Lookup("_id"))
		if o.FilterMode == "not_equal" {
			value = bson.D{{Key: "$ne", Value: value}}
		}
		filter = append(filter, bson.E{Key: "_id", Value: value})
	}
	direction := 1
	if o.SortColumn != "" && o.SortColumn != "_id" {
		return nil, nil, contract.ErrUnsupported
	}
	if o.SortDescending {
		direction = -1
	}
	return filter, options.Find().SetSkip(o.Offset).SetLimit(int64(o.Limit + 1)).SetSort(bson.D{{Key: "_id", Value: direction}}).SetBatchSize(int32(min(o.Limit+1, 100))), nil
}
func lenMustElements(raw bson.Raw) int { e, _ := raw.Elements(); return len(e) }
func (s *Session) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if id != s.ProfileID() {
		return nil, connections.ErrInvalid
	}
	object := o.Object
	if object.ProfileID == 0 {
		object = s.Namespace(db).Object("collection", name)
	}
	if err := s.object(object); err != nil || object.Catalog != db || object.Name != name {
		return nil, connections.ErrInvalid
	}
	filter, opts, err := s.findOptions(o)
	if err != nil {
		return nil, err
	}
	structure, err := s.TableMetadata(ctx, object)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	start := time.Now()
	cur, err := s.client.Database(db).Collection(name).Find(ctx, filter, opts)
	if err != nil {
		return nil, safeError(err)
	}
	defer cur.Close(ctx)
	p := &contract.DataPage{Object: object, Database: db, Table: name, Columns: []string{"_id", documentField}, ColumnKinds: []string{"bson", "json"}, ColumnMetadata: columns(), Offset: o.Offset, Limit: o.Limit, Editable: &structure.Editable, StableOrder: object.Kind == "collection", QuerySQL: "MongoDB find; sort by _id; skip/limit"}
	for cur.Next(ctx) {
		if len(p.Rows) >= o.Limit {
			p.HasMore = true
			break
		}
		values, err := previews(cur.Current)
		if err != nil {
			return nil, err
		}
		var snap *contract.RowSnapshot
		if cur.Current.Lookup("_id").Type != 0 {
			snap, err = s.snapshot(object, cur.Current)
			if err != nil {
				return nil, err
			}
		}
		p.Rows = append(p.Rows, values)
		var ref *contract.RowRef
		if snap != nil {
			ref = &snap.Ref
		}
		p.RowRefs = append(p.RowRefs, ref)
		p.RowSnapshots = append(p.RowSnapshots, snap)
	}
	if err = cur.Err(); err != nil {
		return nil, safeError(err)
	}
	p.CompletedAt = time.Now()
	p.Duration = time.Since(start)
	return p, nil
}
func (s *Session) refFilter(o contract.ObjectRef, ref contract.RowRef) (bson.Raw, error) {
	if ref.Object != o || ref.Database != o.Catalog || ref.Table != o.Name || len(ref.Values) != 1 || len(ref.Columns) != 1 || ref.Columns[0] != "_id" {
		return nil, connections.ErrInvalid
	}
	raw := bson.Raw(ref.Values[0])
	if raw.Validate() != nil || lenMustElements(raw) != 1 || raw.Lookup("_id").Type == 0 {
		return nil, connections.ErrInvalid
	}
	return raw, nil
}
func (s *Session) row(ctx context.Context, id int64, db, name string, ref contract.RowRef) (*contract.DataRow, error) {
	if id != s.ProfileID() {
		return nil, connections.ErrInvalid
	}
	o := ref.Object
	if err := s.object(o); err != nil || o.Catalog != db || o.Name != name {
		return nil, connections.ErrInvalid
	}
	filter, err := s.refFilter(o, ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var raw bson.Raw
	if err = s.client.Database(db).Collection(name).FindOne(ctx, filter).Decode(&raw); err != nil {
		return nil, safeError(err)
	}
	text, err := extendedJSON(raw)
	if err != nil {
		return nil, err
	}
	if len(text) > contract.DataEditMaxBytes {
		return nil, errors.New("complete MongoDB document exceeds 1 MiB editing limit")
	}
	idRaw, err := idDocument(raw)
	if err != nil {
		return nil, err
	}
	idText, err := extendedJSON(idRaw)
	if err != nil {
		return nil, err
	}
	snap, err := s.snapshot(o, raw)
	if err != nil {
		return nil, err
	}
	return &contract.DataRow{Columns: columns(), Snapshot: snap, Values: map[string]contract.DataValue{"_id": {Kind: contract.JSON, Text: idText}, documentField: {Kind: contract.JSON, Text: text}}}, nil
}
func (s *Session) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if id != s.ProfileID() {
		return 0, connections.ErrInvalid
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	n, err := s.client.Database(db).Collection(name).CountDocuments(ctx, bson.D{})
	return n, safeError(err)
}
func (s *Session) chunk(ctx context.Context, id int64, db, name, field string, ref contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if field != documentField && field != "_id" || offset < 0 || units < 1 || units > previewUnits {
		return nil, contract.ErrUnsupported
	}
	if id != s.ProfileID() || ref.Object.Catalog != db || ref.Object.Name != name {
		return nil, connections.ErrInvalid
	}
	filter, err := s.refFilter(ref.Object, ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var raw bson.Raw
	if err = s.client.Database(db).Collection(name).FindOne(ctx, filter).Decode(&raw); err != nil {
		return nil, safeError(err)
	}
	if field == "_id" {
		raw, err = idDocument(raw)
		if err != nil {
			return nil, err
		}
	}
	text, err := extendedJSON(raw)
	if err != nil {
		return nil, err
	}
	runes := []rune(text)
	start := min(offset, int64(len(runes)))
	end := min(start+int64(units), int64(len(runes)))
	return &contract.FieldChunk{Text: string(runes[start:end]), Offset: offset, NextOffset: end, TotalUnits: int64(len(runes)), HasMore: end < int64(len(runes))}, nil
}
func (s *Session) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if err := s.object(o); err != nil {
		return err
	}
	ctx, cancel, err := s.request(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	cur, err := s.client.Database(o.Catalog).Collection(o.Name).Find(ctx, bson.D{}, options.Find().SetBatchSize(100))
	if err != nil {
		return safeError(err)
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		text, err := extendedJSON(cur.Current)
		if err != nil {
			return err
		}
		if _, err = io.WriteString(w, text+"\n"); err != nil {
			return err
		}
	}
	return safeError(cur.Err())
}

func isSystemCollection(name string) bool { return strings.HasPrefix(name, "system.") }

var _ contract.DataReadsProvider = (*Session)(nil)
var _ contract.DataEditsProvider = (*Session)(nil)
var _ contract.DataExportsProvider = (*Session)(nil)
var _ = mongo.ErrNoDocuments

package solr

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type dataLease struct {
	mu, pageMu      sync.Mutex
	session         *Session
	object          contract.ObjectRef
	ctx             context.Context
	cancel          context.CancelFunc
	work            sync.WaitGroup
	closed, changed bool
	done            chan struct{}
	key             [32]byte
	binding         *dataBinding
	epoch           uint64
	expression      *contract.FilterExpression[contract.DataFilterRule]
	rules           []byte
	limit           int
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{})}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	l.ctx, l.cancel = context.WithCancel(context.Background())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.datas) >= 32 {
		l.cancel()
		return nil, ErrBounds
	}
	if s.datas == nil {
		s.datas = map[*dataLease]bool{}
	}
	s.datas[l] = true
	return l, nil
}
func (l *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	l.work.Add(1)
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	l.mu.Unlock()
	child, done, err := l.session.request(child)
	if err != nil {
		stop()
		cancel()
		l.work.Done()
		return nil, nil, err
	}
	return child, func() { done(); stop(); cancel(); l.work.Done() }, nil
}
func (l *dataLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.work.Wait()
	l.binding, l.expression, l.rules = nil, nil, nil
	clear(l.key[:])
	l.session.mu.Lock()
	delete(l.session.datas, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}
func (l *dataLease) identity(id int64, db, name string) error {
	if id != l.object.ProfileID || db != l.object.Catalog || name != l.object.Name {
		return ErrInvalid
	}
	return l.session.checkObject(l.object)
}
func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: l.session.DataDocumentCapabilities(), AdvancedLoader: l.page, RowReader: l.row, Count: l.count, Chunk: l.chunk, Exporter: l.exportRows, JSONL: l.jsonl, TableMetadata: l.metadata, TableInfo: l.info, FilterModes: []string{"equal", "not_equal"}, FilterColumns: []string{"_key"}, SortColumns: []string{}, ExportFormats: []string{"csv", "tsv", "json", "jsonl", "xml", "html", "xlsx"}}
}
func (*Session) DataDocumentCapabilities() contract.Capabilities {
	return contract.Capabilities{Documents: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}
}
func (l *dataLease) sign(v any) string {
	raw, _ := json.Marshal(v)
	h := hmac.New(sha256.New, l.key[:])
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

type position struct {
	Unit int
	Mark string
}

func (l *dataLease) boundary(db string, offset int64, p position) *contract.DataCursor {
	raw, _ := json.Marshal(struct {
		Epoch    uint64
		Position position
	}{l.epoch, p})
	c := &contract.DataCursor{Database: db, Table: l.object.Name, Offset: offset, Columns: []string{"_solr_live_cursor"}, Descending: []bool{false}, Values: [][]byte{raw}}
	c.RuleSignature = l.sign(struct {
		Cursor *contract.DataCursor
		Rules  []byte
		Limit  int
	}{c, l.rules, l.limit})
	return c
}
func (l *dataLease) decodeBoundary(db string, o contract.DataOptions) (position, error) {
	c := o.After
	if c == nil || c.Database != db || c.Table != l.object.Name || c.Offset != o.Offset || o.Limit != l.limit || len(c.Values) != 1 || c.FilterColumn != "" || c.FilterMode != "" || c.FilterValue != "" || c.SortColumn != "" || c.SortDescending || !slices.Equal(c.Columns, []string{"_solr_live_cursor"}) || !slices.Equal(c.Descending, []bool{false}) {
		return position{}, ErrInvalid
	}
	copy := *c
	copy.RuleSignature = ""
	expected := l.sign(struct {
		Cursor *contract.DataCursor
		Rules  []byte
		Limit  int
	}{&copy, l.rules, l.limit})
	if !hmac.Equal([]byte(expected), []byte(c.RuleSignature)) {
		return position{}, ErrInvalid
	}
	var value struct {
		Epoch    uint64
		Position position
	}
	if decodeJSON(c.Values[0], &value) != nil || value.Epoch != l.epoch || value.Position.Unit < 0 || value.Position.Unit >= len(l.binding.Units) || value.Position.Mark == "" || len(value.Position.Mark) > 8192 {
		return position{}, ErrInvalid
	}
	return value.Position, nil
}

// batch visits each captured member/shard separately. It never merges equal keys
// across native shards, and cursorMark stays a live boundary, not a snapshot.
func (l *dataLease) batch(ctx context.Context, c *Client, b *dataBinding, e *contract.FilterExpression[contract.DataFilterRule], p position, limit int) ([]unitProjection, position, bool, error) {
	out := make([]unitProjection, 0, limit)
	for p.Unit < len(b.Units) {
		remaining := limit - len(out)
		if remaining == 0 {
			remaining = 1
		}
		page, err := c.selectProjection(ctx, l.object.Name, b.Units[p.Unit], e, p.Mark, remaining)
		if err != nil {
			return nil, p, false, err
		}
		if len(out) == limit && len(page.Documents) > 0 {
			return out, p, true, nil
		}
		if len(page.Documents) == 0 {
			p.Unit++
			p.Mark = "*"
			continue
		}
		for _, document := range page.Documents {
			out = append(out, unitProjection{Unit: b.Units[p.Unit], Document: document})
		}
		p.Mark = page.Mark
	}
	return out, p, false, nil
}

type unitProjection struct {
	Unit     readUnit
	Document projection
}

func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if o.Object != l.object {
		return nil, ErrInvalid
	}
	e, err := dataExpression(o)
	if err != nil {
		return nil, err
	}
	rules, _ := json.Marshal(e)
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.pageMu.Lock()
	defer l.pageMu.Unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	c, err := l.session.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	p := position{Mark: "*"}
	if o.After == nil {
		if o.Offset != 0 {
			return nil, contract.ErrUnsupported
		}
		l.expression, l.rules, l.limit = e, bytes.Clone(rules), o.Limit
		l.epoch++
	} else {
		if !bytes.Equal(rules, l.rules) {
			return nil, ErrInvalid
		}
		p, err = l.decodeBoundary(db, o)
		if err != nil {
			return nil, err
		}
	}
	started := time.Now()
	documents, next, more, err := l.batch(ctx, c, b, e, p, o.Limit)
	if err != nil {
		return nil, err
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	editable := false
	out := &contract.DataPage{Object: l.object, Database: db, Table: name, Columns: dataFields(), ColumnKinds: []string{"text", "text", "native key", "integer", "json"}, ColumnMetadata: dataColumns(), Offset: o.Offset, Limit: o.Limit, HasMore: more, StableOrder: true, KeysetAvailable: true, Editable: &editable, QuerySQL: "Solr native stored/docValues projection; live cursorMark per original member/shard, not PIT"}
	out.CurrentCursor = l.boundary(db, o.Offset, p)
	if more {
		out.NextCursor = l.boundary(db, o.Offset+int64(len(documents)), next)
	}
	for _, document := range documents {
		values := projectionCells(document)
		row := make([]contract.DataCell, len(values))
		for i, value := range values {
			runes := []rune(value.Text)
			if len(runes) > 1024 {
				value.Text = string(runes[:1024])
				value.Truncated = true
			}
			copy := value
			row[i] = contract.DataCell{Text: value.Text, Truncated: value.Truncated, Value: &copy}
		}
		snapshot := l.snapshot(db, document)
		out.Rows = append(out.Rows, row)
		out.RowRefs = append(out.RowRefs, &snapshot.Ref)
		out.RowSnapshots = append(out.RowSnapshots, snapshot)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	out.CompletedAt, out.Duration = time.Now(), time.Since(started)
	return out, nil
}
func dataFields() []string { return []string{"_member", "_shard", "_key", "_version_", "document"} }
func dataColumns() []contract.StructureColumn {
	return []contract.StructureColumn{
		{Name: "_member", Type: "native member", DataType: "text", ValueKind: contract.Text, Generated: true, Position: 1},
		{Name: "_shard", Type: "native shard", DataType: "text", ValueKind: contract.Text, Generated: true, Position: 2},
		{Name: "_key", Type: "native uniqueKey", DataType: "native key", Generated: true, Position: 3},
		{Name: "_version_", Type: "native version", DataType: "integer", ValueKind: contract.Integer, Generated: true, Position: 4},
		{Name: "document", Type: "native stored/docValues projection", DataType: "json", ValueKind: contract.JSON, Generated: true, Position: 5, Comment: "Native returned fields, including copy fields; not the original submitted source"},
	}
}
func projectionCells(v unitProjection) []contract.Cell {
	return []contract.Cell{{Kind: contract.Text, Text: v.Unit.Member}, {Kind: contract.Text, Text: v.Unit.Shard}, v.Document.Key, {Kind: contract.Integer, Text: v.Document.Version.String()}, {Kind: contract.JSON, Text: string(v.Document.Document)}}
}

type projectionIdentity struct {
	Object                             contract.ObjectRef
	Database                           string
	Unit                               readUnit
	Key                                contract.Cell
	Version                            json.Number
	Projection, Fingerprint, Signature string
}

func (l *dataLease) snapshot(db string, v unitProjection) *contract.RowSnapshot {
	identity := projectionIdentity{Object: l.object, Database: db, Unit: v.Unit, Key: v.Document.Key, Version: v.Document.Version, Projection: projectionFingerprint(v.Document.Document), Fingerprint: l.binding.Metadata.Fingerprint}
	identity.Signature = l.sign(identity)
	raw, _ := json.Marshal(identity)
	ref := contract.RowRef{Object: l.object, Database: db, Table: l.object.Name, Columns: []string{"_solr_projection_identity"}, Values: [][]byte{raw}, TypedValues: []contract.Cell{v.Document.Key}}
	snapshot := &contract.RowSnapshot{Mode: "fingerprints", Ref: ref, Columns: dataFields(), Metadata: string(raw)}
	for _, cell := range projectionCells(v) {
		value := fingerprint([]byte(cell.Text))
		snapshot.Fingerprints = append(snapshot.Fingerprints, &value)
	}
	return snapshot
}
func (l *dataLease) ref(db string, r contract.RowRef) (projectionIdentity, error) {
	var identity projectionIdentity
	if r.Object != l.object || r.Database != db || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_solr_projection_identity"}) || len(r.Values) != 1 || len(r.Values[0]) > 16384 || len(r.TypedValues) != 1 {
		return identity, ErrInvalid
	}
	if decodeJSON(r.Values[0], &identity) != nil {
		return identity, ErrInvalid
	}
	signature := identity.Signature
	identity.Signature = ""
	expected := l.sign(identity)
	identity.Signature = signature
	if !hmac.Equal([]byte(signature), []byte(expected)) || identity.Object != l.object || identity.Database != db || r.TypedValues[0].Kind != identity.Key.Kind || r.TypedValues[0].Text != identity.Key.Text || r.TypedValues[0].Truncated || r.TypedValues[0].Bytes != nil {
		return identity, ErrInvalid
	}
	return identity, nil
}
func (l *dataLease) row(ctx context.Context, id int64, db, name string, r contract.RowRef) (*contract.DataRow, error) {
	return l.readRow(ctx, id, db, name, r, false)
}
func (l *dataLease) readRow(ctx context.Context, id int64, db, name string, r contract.RowRef, allowLarge bool) (*contract.DataRow, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	identity, err := l.ref(db, r)
	if err != nil {
		return nil, err
	}
	c, err := l.session.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	if b.Metadata.Fingerprint != identity.Fingerprint || !slices.Contains(b.Units, identity.Unit) {
		return nil, ErrDataTargetChanged
	}
	rule := contract.DataFilterRule{Column: "_key", Mode: "equal", Value: identity.Key.Text}
	e := &contract.FilterExpression[contract.DataFilterRule]{Op: "rule", Rule: &rule}
	page, err := c.selectProjection(ctx, l.object.Name, identity.Unit, e, "", 2)
	if err != nil {
		return nil, err
	}
	if page.Total != 1 || len(page.Documents) != 1 || !sameKey(page.Documents[0].Key, identity.Key) || page.Documents[0].Version != identity.Version || projectionFingerprint(page.Documents[0].Document) != identity.Projection {
		return nil, contract.ErrDataConflict
	}
	if _, err = l.guard(ctx, c); err != nil {
		return nil, err
	}
	v := unitProjection{Unit: identity.Unit, Document: page.Documents[0]}
	if !allowLarge && len(v.Document.Document) > 1<<20 {
		return nil, ErrBounds
	}
	snapshot := l.snapshot(db, v)
	snapshot.Mode = "values"
	snapshot.Fingerprints = nil
	snapshot.Original = projectionCells(v)
	out := &contract.DataRow{Columns: dataColumns(), Snapshot: snapshot, Values: map[string]contract.DataValue{}}
	for i, cell := range snapshot.Original {
		out.Values[dataFields()[i]] = contract.DataValue{Kind: cell.Kind, Text: cell.Text}
	}
	return out, ctx.Err()
}
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if !slices.Contains(dataFields(), field) || offset < 0 || units < 1 || units > 1024 {
		return nil, contract.ErrUnsupported
	}
	row, err := l.readRow(ctx, id, db, name, r, true)
	if err != nil {
		return nil, err
	}
	runes := []rune(row.Values[field].Text)
	start := min(offset, int64(len(runes)))
	end := min(start+int64(units), int64(len(runes)))
	return &contract.FieldChunk{Text: string(runes[start:end]), Offset: offset, NextOffset: end, TotalUnits: int64(len(runes)), HasMore: end < int64(len(runes))}, nil
}
func (l *dataLease) count(ctx context.Context, id int64, db, name string) (int64, error) {
	if err := l.identity(id, db, name); err != nil {
		return 0, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	c, err := l.session.open(ctx)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	b, err := l.guard(ctx, c)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, unit := range b.Units {
		page, err := c.selectProjection(ctx, l.object.Name, unit, nil, "", 0)
		if err != nil {
			return 0, err
		}
		if total > math.MaxInt64-page.Total {
			return 0, ErrBounds
		}
		total += page.Total
	}
	if _, err = l.guard(ctx, c); err != nil {
		return 0, err
	}
	return total, ctx.Err()
}
func (l *dataLease) metadata(ctx context.Context, id int64, db, name string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	c, err := l.session.open(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	b, err := l.guard(ctx, c)
	if err != nil {
		return nil, err
	}
	out := &contract.TableStructure{Object: l.object, Database: db, Name: name, Kind: l.object.Kind, Engine: "Solr", Columns: dataColumns(), Definition: string(b.Metadata.Target.Definition), Comment: "Read-only native stored/docValues projection; live member/shard cursor, not source/PIT"}
	for _, member := range b.Metadata.Members {
		out.Definitions = append(out.Definitions, contract.Definition{Name: member.Name, Kind: "schema", SQL: string(member.Schema)}, contract.Definition{Name: member.Name, Kind: "config", SQL: string(member.Config)})
	}
	return out, ctx.Err()
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: m.Kind, Engine: m.Engine, Comment: m.Comment}, nil
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if err := l.identity(id, db, name); err != nil {
		return err
	}
	if o.Object != l.object || sink == nil {
		return ErrInvalid
	}
	if o.Offset != 0 || o.After != nil {
		return contract.ErrUnsupported
	}
	if len(fields) == 0 {
		fields = dataFields()
	}
	fields = append([]string(nil), fields...)
	for _, field := range fields {
		if !slices.Contains(dataFields(), field) {
			return contract.ErrUnsupported
		}
	}
	o.Limit = 100
	e, err := dataExpression(o)
	if err != nil {
		return err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	c, err := l.session.open(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	b, err := l.guard(ctx, c)
	if err != nil {
		return err
	}
	p := position{Mark: "*"}
	for {
		documents, next, more, err := l.batch(ctx, c, b, e, p, 100)
		if err != nil {
			return err
		}
		if _, err = l.guard(ctx, c); err != nil {
			return err
		}
		for _, document := range documents {
			if err = ctx.Err(); err != nil {
				return err
			}
			cells := projectionCells(document)
			values := make([]contract.DataValue, len(fields))
			for i, field := range fields {
				cell := cells[slices.Index(dataFields(), field)]
				values[i] = contract.DataValue{Kind: cell.Kind, Text: cell.Text}
			}
			if err = sink(values); err != nil {
				return err
			}
		}
		if _, err = l.guard(ctx, c); err != nil {
			return err
		}
		if !more {
			return ctx.Err()
		}
		p = next
	}
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object || w == nil {
		return ErrInvalid
	}
	return l.exportRows(ctx, o.ProfileID, o.Catalog, o.Name, contract.DataOptions{Object: o}, dataFields(), func(values []contract.DataValue) error {
		// Include native member/shard/key/version; repeated keys in other shards stay
		// distinct. The projection is not presented as original submitted source.
		document := struct {
			Member, Shard string
			Key           contract.Cell
			Version       json.Number
			Projection    json.RawMessage
		}{values[0].Text, values[1].Text, contract.Cell{Kind: values[2].Kind, Text: values[2].Text}, json.Number(values[3].Text), json.RawMessage(values[4].Text)}
		raw, err := json.Marshal(document)
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		n, err := w.Write(raw)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		return err
	})
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

func sameKey(a, b contract.Cell) bool {
	return a.Kind == b.Kind && a.Text == b.Text && !a.Truncated && !b.Truncated && a.Bytes == nil && b.Bytes == nil
}
func projectionFingerprint(raw []byte) string {
	canonical, err := canonical(raw)
	if err != nil {
		return ""
	}
	return fingerprint(canonical)
}

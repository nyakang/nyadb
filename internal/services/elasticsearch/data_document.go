package elasticsearch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
)

// dataLease owns the PIT independently of whether a worker's page is published.
// Boundaries and row identities are signed for this immutable document only.
type dataLease struct {
	mu       sync.Mutex
	pageMu   sync.Mutex
	session  *Session
	object   contract.ObjectRef
	ctx      context.Context
	cancel   context.CancelFunc
	work     sync.WaitGroup
	closed   bool
	done     chan struct{}
	closeErr error
	key      [32]byte
	client   *Client
	cursor   *Cursor
	epoch    uint64
	query    json.RawMessage
	limit    int
	members  map[string]string // concrete index name -> original index UUID
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
		s.datas = make(map[*dataLease]bool)
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
	child, sessionDone, err := l.session.request(child)
	if err != nil {
		stop()
		cancel()
		l.work.Done()
		return nil, nil, err
	}
	return child, func() { sessionDone(); stop(); cancel(); l.work.Done() }, nil
}
func (l *dataLease) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return l.closeErr
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	l.work.Wait()
	if l.client != nil {
		l.closeErr = l.client.Close()
	}
	l.client, l.cursor, l.query, l.members = nil, nil, nil, nil
	clear(l.key[:])
	l.session.mu.Lock()
	delete(l.session.datas, l)
	l.session.mu.Unlock()
	close(l.done)
	return l.closeErr
}
func (l *dataLease) identity(id int64, db, name string) error {
	if id != l.object.ProfileID || name != l.object.Name || db != l.session.identity.ClusterUUID && db != l.session.identity.ClusterName {
		return ErrInvalid
	}
	return l.session.checkObject(l.object)
}
func (l *dataLease) Runtime() contract.DataRuntime {
	// This internal binding is verified before public provider registration.
	// Editing is added separately; no product entry advertises this partial stage.
	return contract.DataRuntime{Capabilities: contract.Capabilities{Documents: true, Metadata: true, Keyset: true, Count: true, FieldChunks: true, Export: true, JSONL: true}, AdvancedLoader: l.page, RowReader: l.row, Count: l.count, Chunk: l.chunk, Exporter: l.exportRows, JSONL: l.jsonl, TableMetadata: l.metadata, TableInfo: l.info, FilterModes: []string{"equal", "not_equal"}, ExportFormats: []string{"csv", "tsv", "json", "jsonl", "xml", "html", "xlsx"}}
}
func (l *dataLease) sign(v any) string {
	raw, _ := json.Marshal(v)
	m := hmac.New(sha256.New, l.key[:])
	_, _ = m.Write(raw)
	return hex.EncodeToString(m.Sum(nil))
}

func documentQuery(o contract.DataOptions) (json.RawMessage, error) {
	if o.Offset < 0 || o.Limit < 1 || o.Limit > 500 || o.SortColumn != "" || o.SortDescending || len(o.Sorts) != 0 {
		return nil, contract.ErrUnsupported
	}
	if o.Condition == nil && o.FilterMode == "" && (o.FilterColumn != "" || o.FilterValue != "") {
		return nil, ErrInvalid
	}
	expression, err := contract.EffectiveDataExpression(o)
	if err != nil {
		return nil, err
	}
	var compile func(*contract.FilterExpression[contract.DataFilterRule]) (any, error)
	compile = func(e *contract.FilterExpression[contract.DataFilterRule]) (any, error) {
		if e == nil {
			return map[string]any{"match_all": map[string]any{}}, nil
		}
		if e.Rule != nil {
			r := *e.Rule
			if r.Column != "_id" && r.Column != "_routing" || r.Mode != "equal" && r.Mode != "not_equal" || len(r.Value) > 512 {
				return nil, contract.ErrUnsupported
			}
			term := map[string]any{"term": map[string]string{r.Column: r.Value}}
			if r.Mode == "not_equal" {
				return map[string]any{"bool": map[string]any{"must_not": []any{term}}}, nil
			}
			return term, nil
		}
		parts := make([]any, 0, len(e.Children))
		for i := range e.Children {
			p, err := compile(&e.Children[i])
			if err != nil {
				return nil, err
			}
			parts = append(parts, p)
		}
		if e.Op == "or" {
			return map[string]any{"bool": map[string]any{"should": parts, "minimum_should_match": 1}}, nil
		}
		return map[string]any{"bool": map[string]any{"filter": parts}}, nil
	}
	query, err := compile(expression)
	if err != nil {
		return nil, err
	}
	return json.Marshal(query)
}

func indexUUID(ctx context.Context, c *Client, index string) (string, error) {
	r, err := c.Do(ctx, Request{Method: "GET", Path: "/" + url.PathEscape(index) + "/_settings/index.uuid?flat_settings=true"})
	if err != nil {
		return "", err
	}
	var root map[string]struct {
		Settings map[string]string `json:"settings"`
	}
	if decodeJSON(r.Body, &root) != nil || len(root) != 1 || root[index].Settings["index.uuid"] == "" {
		return "", ErrProtocol
	}
	uuid := root[index].Settings["index.uuid"]
	if len(uuid) > 128 {
		return "", ErrProtocol
	}
	return uuid, nil
}
func (l *dataLease) resolve(ctx context.Context, c *Client) (map[string]string, error) {
	targets, err := c.ResolveTargets(ctx, l.object.Name)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if target.Name == l.object.Name && target.Kind == l.object.Kind {
			members := make(map[string]string, len(target.Members))
			for _, name := range target.Members {
				uuid, err := indexUUID(ctx, c, name)
				if err != nil {
					return nil, err
				}
				members[name] = uuid
			}
			return members, nil
		}
	}
	return nil, ErrInvalid
}
func sameMembers(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (l *dataLease) boundary(db string, offset int64, after []json.RawMessage) *contract.DataCursor {
	c := &contract.DataCursor{Database: db, Table: l.object.Name, Offset: offset, Columns: []string{"_shard_doc"}, Descending: []bool{false}}
	epoch, _ := json.Marshal(l.epoch)
	c.Values = append(c.Values, epoch)
	for _, v := range after {
		c.Values = append(c.Values, bytes.Clone(v))
	}
	c.RuleSignature = l.sign(struct {
		Cursor *contract.DataCursor
		Query  json.RawMessage
		Limit  int
	}{c, l.query, l.limit})
	return c
}
func (l *dataLease) decodeBoundary(db string, o contract.DataOptions) ([]json.RawMessage, error) {
	c := o.After
	if c == nil || c.Database != db || c.Table != l.object.Name || c.Offset != o.Offset || len(c.Values) < 1 || len(c.Values) > 2 || c.FilterColumn != "" || c.FilterMode != "" || c.FilterValue != "" || c.SortColumn != "" || c.SortDescending || !slices.Equal(c.Columns, []string{"_shard_doc"}) || !slices.Equal(c.Descending, []bool{false}) {
		return nil, ErrInvalid
	}
	var epoch uint64
	if json.Unmarshal(c.Values[0], &epoch) != nil || epoch != l.epoch || o.Limit != l.limit {
		return nil, ErrInvalid
	}
	copy := *c
	copy.RuleSignature = ""
	expected := l.sign(struct {
		Cursor *contract.DataCursor
		Query  json.RawMessage
		Limit  int
	}{&copy, l.query, l.limit})
	if !hmac.Equal([]byte(expected), []byte(c.RuleSignature)) {
		return nil, ErrInvalid
	}
	if o.Offset > 0 && len(c.Values) != 2 || o.Offset == 0 && len(c.Values) != 1 {
		return nil, ErrInvalid
	}
	var after []json.RawMessage
	for _, v := range c.Values[1:] {
		after = append(after, bytes.Clone(v))
	}
	return after, nil
}
func (l *dataLease) page(ctx context.Context, id int64, db, name string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, name); err != nil {
		return nil, err
	}
	if o.Object != l.object {
		return nil, ErrInvalid
	}
	query, err := documentQuery(o)
	if err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l.pageMu.Lock()
	defer l.pageMu.Unlock()
	started := time.Now()
	var after []json.RawMessage
	if o.After == nil {
		if o.Offset != 0 {
			return nil, contract.ErrUnsupported
		}
		if l.client != nil {
			if err = l.client.Close(); err != nil {
				return nil, err
			}
			l.client, l.cursor = nil, nil
		}
		c, err := l.session.open(ctx)
		if err != nil {
			return nil, err
		}
		l.client = c
		members, err := l.resolve(ctx, c)
		if err != nil {
			return nil, err
		}
		cursor, err := c.OpenCursor(ctx, name, query, o.Limit)
		if err != nil {
			return nil, err
		}
		l.cursor = cursor
		actual, err := l.resolve(ctx, c)
		if err != nil {
			return nil, err
		}
		if !sameMembers(members, actual) {
			return nil, errors.New("Elasticsearch target changed while opening its snapshot")
		}
		l.query, l.limit = query, o.Limit
		l.epoch++
		l.mu.Lock()
		l.members = members
		l.mu.Unlock()
	} else {
		if l.cursor == nil || !bytes.Equal(query, l.query) {
			return nil, ErrInvalid
		}
		after, err = l.decodeBoundary(db, o)
		if err != nil {
			return nil, err
		}
	}
	page, err := l.cursor.ReadAfter(ctx, after, o.Limit)
	if err != nil {
		return nil, err
	}
	editable := false
	out := &contract.DataPage{Object: l.object, Database: db, Table: name, Columns: documentFields(), ColumnMetadata: documentColumns(), ColumnKinds: []string{"text", "text", "text", "json"}, Offset: o.Offset, Limit: o.Limit, HasMore: page.HasMore, StableOrder: true, KeysetAvailable: true, Editable: &editable, QuerySQL: "Elasticsearch PIT search; original _shard_doc boundary"}
	out.CurrentCursor = l.boundary(db, o.Offset, after)
	if page.HasMore {
		out.NextCursor = l.boundary(db, o.Offset+int64(len(page.Documents)), page.After)
	}
	for _, document := range page.Documents {
		l.mu.Lock()
		uuid := l.members[document.Identity.Index]
		l.mu.Unlock()
		if uuid == "" {
			return nil, ErrProtocol
		}
		snapshot := l.snapshot(db, document, uuid)
		out.Rows = append(out.Rows, documentPreviews(document))
		out.RowRefs = append(out.RowRefs, &snapshot.Ref)
		out.RowSnapshots = append(out.RowSnapshots, snapshot)
	}
	out.CompletedAt = time.Now()
	out.Duration = time.Since(started)
	return out, nil
}

func documentFields() []string { return []string{"_index", "_id", "_routing", "document"} }
func documentColumns() []contract.StructureColumn {
	return []contract.StructureColumn{
		{Name: "_index", Type: "text", DataType: "text", ValueKind: contract.Text, Generated: true, Position: 1},
		{Name: "_id", Type: "text", DataType: "text", ValueKind: contract.Text, Generated: true, Position: 2},
		{Name: "_routing", Type: "text", DataType: "text", ValueKind: contract.Text, Position: 3, Nullable: true},
		{Name: "document", Type: "JSON _source", DataType: "json", ValueKind: contract.JSON, Position: 4, Comment: "Complete original _source JSON; numeric literals are preserved"},
	}
}
func sourceFingerprint(source []byte) string {
	var b bytes.Buffer
	_ = json.Compact(&b, source)
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}
func documentCells(s Snapshot) []contract.Cell {
	return []contract.Cell{{Kind: contract.Text, Text: s.Identity.Index}, {Kind: contract.Text, Text: s.Identity.ID}, {Kind: contract.Text, Text: s.Identity.Routing}, {Kind: contract.JSON, Text: string(s.Source)}}
}
func documentPreviews(s Snapshot) []contract.DataCell {
	values := documentCells(s)
	out := make([]contract.DataCell, len(values))
	for i, c := range values {
		r := []rune(c.Text)
		if len(r) > 1024 {
			c.Text = string(r[:1024])
			c.Truncated = true
		}
		out[i] = contract.DataCell{Text: c.Text, Truncated: c.Truncated, Value: &c}
	}
	return out
}

type rowIdentity struct {
	Object    contract.ObjectRef
	Database  string
	ID        DocumentID
	UUID      string
	Signature string
}
type rowVersion struct {
	Sequence, PrimaryTerm int64
	Signature             string
}

func (l *dataLease) snapshot(db string, s Snapshot, uuid string) *contract.RowSnapshot {
	identity := rowIdentity{Object: l.object, Database: db, ID: s.Identity, UUID: uuid}
	identity.Signature = l.sign(identity)
	raw, _ := json.Marshal(identity)
	out := &contract.RowSnapshot{Mode: "fingerprints", Ref: contract.RowRef{Object: l.object, Database: db, Table: l.object.Name, Columns: []string{"_index", "_id", "_routing", "_identity"}, Values: [][]byte{[]byte(s.Identity.Index), []byte(s.Identity.ID), []byte(s.Identity.Routing), raw}}, Columns: documentFields()}
	for _, c := range documentCells(s) {
		fp := sourceFingerprint(s.Source)
		if c.Kind != contract.JSON {
			sum := sha256.Sum256([]byte(c.Text))
			fp = hex.EncodeToString(sum[:])
		}
		out.Fingerprints = append(out.Fingerprints, &fp)
	}
	v := rowVersion{Sequence: s.Sequence, PrimaryTerm: s.PrimaryTerm}
	v.Signature = l.sign(struct {
		Snapshot *contract.RowSnapshot
		Version  rowVersion
	}{out, v})
	encoded, _ := json.Marshal(v)
	out.Metadata = string(encoded)
	return out
}
func (l *dataLease) ref(db string, r contract.RowRef) (rowIdentity, error) {
	var identity rowIdentity
	if r.Object != l.object || r.Database != db || r.Table != l.object.Name || !slices.Equal(r.Columns, []string{"_index", "_id", "_routing", "_identity"}) || len(r.Values) != 4 || len(r.TypedValues) != 0 || decodeJSON(r.Values[3], &identity) != nil {
		return identity, ErrInvalid
	}
	signature := identity.Signature
	identity.Signature = ""
	expected := l.sign(identity)
	identity.Signature = signature
	if identity.Object != l.object || identity.Database != db || !validDocumentID(identity.ID) || identity.UUID == "" || !hmac.Equal([]byte(signature), []byte(expected)) || string(r.Values[0]) != identity.ID.Index || string(r.Values[1]) != identity.ID.ID || string(r.Values[2]) != identity.ID.Routing {
		return identity, ErrInvalid
	}
	return identity, nil
}
func (l *dataLease) read(ctx context.Context, c *Client, identity rowIdentity) (Snapshot, error) {
	members, err := l.resolve(ctx, c)
	if err != nil {
		return Snapshot{}, err
	}
	if members[identity.ID.Index] != identity.UUID {
		return Snapshot{}, errors.New("Elasticsearch original concrete index no longer exists")
	}
	query, _ := json.Marshal(map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"term": map[string]string{"_index": identity.ID.Index}}, map[string]any{"term": map[string]string{"_id": identity.ID.ID}}, map[string]any{"term": map[string]string{"_routing": identity.ID.Routing}}}}})
	// An unrouted document has no _routing term; constrain its identity after search.
	if identity.ID.Routing == "" {
		query, _ = json.Marshal(map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"term": map[string]string{"_index": identity.ID.Index}}, map[string]any{"term": map[string]string{"_id": identity.ID.ID}}}, "must_not": []any{map[string]any{"exists": map[string]string{"field": "_routing"}}}}})
	}
	cur, err := c.OpenCursor(ctx, l.object.Name, query, 1)
	if err != nil {
		return Snapshot{}, err
	}
	page, err := cur.Next(ctx)
	closeErr := cur.Close(context.WithoutCancel(ctx))
	if err != nil {
		return Snapshot{}, err
	}
	if closeErr != nil {
		return Snapshot{}, closeErr
	}
	if page.Total != 1 || len(page.Documents) != 1 || page.Documents[0].Identity != identity.ID {
		return Snapshot{}, errors.New("Elasticsearch original document is no longer in this target")
	}
	uuid, err := indexUUID(ctx, c, identity.ID.Index)
	if err != nil {
		return Snapshot{}, err
	}
	if uuid != identity.UUID {
		return Snapshot{}, errors.New("Elasticsearch original concrete index changed during read")
	}
	return page.Documents[0], nil
}
func (l *dataLease) row(ctx context.Context, id int64, db, name string, r contract.RowRef) (*contract.DataRow, error) {
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
	document, err := l.read(ctx, c, identity)
	if err != nil {
		return nil, err
	}
	out := &contract.DataRow{Columns: documentColumns(), Snapshot: l.snapshot(db, document, identity.UUID), Values: map[string]contract.DataValue{}}
	for i, cell := range documentCells(document) {
		out.Values[documentFields()[i]] = contract.DataValue{Kind: cell.Kind, Text: cell.Text}
	}
	return out, nil
}
func (l *dataLease) chunk(ctx context.Context, id int64, db, name, field string, r contract.RowRef, offset int64, units int) (*contract.FieldChunk, error) {
	if !slices.Contains(documentFields(), field) || offset < 0 || units < 1 || units > 1024 {
		return nil, contract.ErrUnsupported
	}
	row, err := l.row(ctx, id, db, name, r)
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
	if _, err = l.resolve(ctx, c); err != nil {
		return 0, err
	}
	r, err := c.Do(ctx, Request{Method: "POST", Path: "/" + url.PathEscape(name) + "/_count", Body: json.RawMessage(`{"query":{"match_all":{}}}`)})
	if err != nil {
		return 0, err
	}
	var receipt struct {
		Count  *int64                                  `json:"count"`
		Shards struct{ Total, Successful, Failed int } `json:"_shards"`
	}
	if decodeJSON(r.Body, &receipt) != nil || receipt.Count == nil || *receipt.Count < 0 || receipt.Shards.Total <= 0 || receipt.Shards.Successful != receipt.Shards.Total || receipt.Shards.Failed != 0 {
		return 0, ErrProtocol
	}
	return *receipt.Count, nil
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
	return l.session.TableMetadata(ctx, l.object)
}
func (l *dataLease) info(ctx context.Context, id int64, db, name string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, db, name)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: name, Kind: m.Kind, Engine: "Elasticsearch", Comment: "PIT document snapshot; count is a separate current target read"}, nil
}
func (l *dataLease) exportRows(ctx context.Context, id int64, db, name string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) (err error) {
	if err = l.identity(id, db, name); err != nil {
		return err
	}
	if sink == nil || o.Object != l.object {
		return ErrInvalid
	}
	if len(fields) == 0 {
		fields = documentFields()
	}
	for _, field := range fields {
		if !slices.Contains(documentFields(), field) {
			return contract.ErrUnsupported
		}
	}
	if o.Offset != 0 || o.After != nil {
		return contract.ErrUnsupported
	}
	o.Limit = 10
	query, err := documentQuery(o)
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
	defer func() { err = errors.Join(err, c.Close()) }()
	if _, err = l.resolve(ctx, c); err != nil {
		return err
	}
	cursor, err := c.OpenCursor(ctx, name, query, 10)
	if err != nil {
		return err
	}
	for {
		page, e := cursor.Next(ctx)
		if e != nil {
			return e
		}
		for _, document := range page.Documents {
			values := documentCells(document)
			out := make([]contract.DataValue, len(fields))
			for i, field := range fields {
				cell := values[slices.Index(documentFields(), field)]
				out[i] = contract.DataValue{Kind: cell.Kind, Text: cell.Text}
			}
			if e = sink(out); e != nil {
				return e
			}
		}
		if !page.HasMore {
			break
		}
	}
	return nil
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object || w == nil {
		return ErrInvalid
	}
	return l.exportRows(ctx, o.ProfileID, l.session.identity.ClusterUUID, o.Name, contract.DataOptions{Object: o}, []string{"document"}, func(values []contract.DataValue) error {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(values[0].Text)); err != nil {
			return err
		}
		_, err := io.WriteString(w, compact.String()+"\n")
		return err
	})
}

var _ contract.DataDocumentsProvider = (*Session)(nil)

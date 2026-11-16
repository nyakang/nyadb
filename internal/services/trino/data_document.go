package trino

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// Reservation is pure. Each original document owns cancellation and one-attempt
// plans; an unrelated reader cannot settle a missing write acknowledgement.
type dataLease struct {
	mu                             sync.Mutex
	session                        *Session
	object                         contract.ObjectRef
	ctx                            context.Context
	cancel                         context.CancelFunc
	closed, planning, writeBlocked bool
	done                           chan struct{}
	work                           sync.WaitGroup
	plan                           *dataWritePlan
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, contract.ErrRevoked
	}
	if len(s.dataLeases) >= 64 {
		return nil, contract.ErrSQLSessionLimit
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{})}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	if s.dataLeases == nil {
		s.dataLeases = map[*dataLease]bool{}
	}
	s.dataLeases[l] = true
	return l, nil
}

func (s *Session) DataDocumentCapabilities() contract.Capabilities {
	c := s.Capabilities()
	c.EditableTables = true
	return c
}

func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: l.session.DataDocumentCapabilities(),
		NativeReceipts: true, SequentialWrites: true, InsertOmittedValues: true,
		EditKinds: []string{"insert"}, PreviewEditKinds: []string{"insert"},
		EditValueKinds: []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.Binary, contract.DateTime, contract.JSON},
		PreviewEdits:   l.previewEdits, SubmitEdits: l.submitEdits,
		MutationPresentation: &contract.DataMutationPresentation{Title: "确认 Trino 数据新增", Hint: "各 INSERT 语句独立执行，不提供批次事务。省略字段由原生连接器处理；显式 NULL 与省略不同。当前驱动不提供主键，已有行保持只读。未知或不完整回执保留草稿并禁止重放。", PreviewLabel: "Trino 原生 INSERT 完整预览", HistoryLabel: "已确认的 Trino 数据新增", CreateLabel: "新增记录"},
		ReceiptPresentation:  &contract.NativeWriteReceiptPresentation{ExportFilename: "trino-write-receipt.json"},
		AdvancedLoader:       l.page, TableMetadata: l.metadata, TableInfo: l.info, Count: l.count,
		Exporter: l.export, JSONL: l.jsonl, ExportFormats: l.session.DataExports().Formats,
		FilterModes:   l.session.DataFilterModes(),
		ReadScopeHint: "Trino 分页读取当前可见数据；跨页不提供一致快照。"}
}

func (l *dataLease) identity(id int64, db, table string) error {
	if id != l.object.ProfileID || db != namespaceLabel(l.object.Catalog, l.object.Schema) || table != l.object.Name {
		return connections.ErrInvalid
	}
	return l.session.checkObject(l.object)
}
func (l *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		l.mu.Unlock()
		return nil, nil, err
	}
	l.work.Add(1)
	l.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() { stop(); cancel(); l.work.Done() }, nil
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
	l.mu.Lock()
	if l.plan != nil {
		l.plan.releaseLocked()
		l.plan = nil
	}
	l.mu.Unlock()
	l.session.mu.Lock()
	delete(l.session.dataLeases, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}
func (l *dataLease) metadata(ctx context.Context, id int64, db, table string) (*contract.TableStructure, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := l.session.TableMetadata(ctx, l.object)
	if err == nil {
		t.Editable = strings.EqualFold(t.Kind, "TABLE")
	}
	return t, err
}
func (l *dataLease) page(ctx context.Context, id int64, db, table string, o contract.DataOptions) (*contract.DataPage, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	if o.Object.ProfileID != 0 && o.Object != l.object {
		return nil, connections.ErrInvalid
	}
	o.Object = l.object
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	p, err := l.session.page(ctx, id, db, table, o)
	if err == nil {
		editable := strings.EqualFold(l.object.Kind, "TABLE")
		p.Editable = &editable
		p.ColumnEditKinds = map[string][]string{}
		for _, c := range p.ColumnMetadata {
			if _, e := parseInsertType(c.Type); editable && e == nil {
				p.ColumnEditKinds[c.Name] = []string{"insert"}
			}
		}
	}
	return p, err
}
func (l *dataLease) count(ctx context.Context, id int64, db, table string) (int64, error) {
	if err := l.identity(id, db, table); err != nil {
		return 0, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	return l.session.count(ctx, id, db, table)
}
func (l *dataLease) info(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
	if err := l.identity(id, db, table); err != nil {
		return nil, err
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return l.session.tableInfo(ctx, id, db, table)
}
func (l *dataLease) export(ctx context.Context, id int64, db, table string, o contract.DataOptions, fields []string, sink func([]contract.DataValue) error) error {
	if err := l.identity(id, db, table); err != nil {
		return err
	}
	if o.Object.ProfileID != 0 && o.Object != l.object {
		return connections.ErrInvalid
	}
	o.Object = l.object
	ctx, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	return l.session.export(ctx, id, db, table, o, fields, sink)
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, w io.Writer) error {
	if o != l.object {
		return connections.ErrInvalid
	}
	ctx, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	return l.session.jsonl(ctx, o, w)
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

package prestosql

import (
	"context"
	"strconv"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
	"github.com/nyakang/nyadb/internal/services/jdbcplugin"
)

type dataLease struct {
	mu                             sync.Mutex
	session                        *Session
	object                         contract.ObjectRef
	primary                        *queryLease
	ctx                            context.Context
	cancel                         context.CancelFunc
	work                           sync.WaitGroup
	done                           chan struct{}
	closed, planning, writeBlocked bool
	active                         int
	plan                           *dataWritePlan
	unresolved                     *jdbcplugin.Receipt
}

func (s *Session) dataOwnerLocked(parent context.Context, role string, pool chan struct{}) (*queryLease, error) {
	if s.closed || s.lost || !s.metadata.runtime.Client.Alive() {
		return nil, contract.ErrRevoked
	}
	if s.nextOwner == ^uint64(0) {
		return nil, contract.ErrSQLSessionLimit
	}
	s.nextOwner++
	ctx, cancel := context.WithCancel(parent)
	return &queryLease{session: s, role: role + "-" + strconv.FormatUint(s.nextOwner, 10), ctx: ctx, cancel: cancel, pool: pool}, nil
}
func (s *Session) ReserveDataDocument(object contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkNamespace(object.Namespace()); err != nil {
		return nil, err
	}
	if !validName(object.Name) || object.Kind != "table" && object.Kind != "view" {
		return nil, connections.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dataLeases) >= 32 {
		return nil, contract.ErrSQLSessionLimit
	}
	ctx, cancel := context.WithCancel(s.ctx)
	primary, err := s.dataOwnerLocked(ctx, "data", s.dataSlots)
	if err != nil {
		cancel()
		return nil, err
	}
	l := &dataLease{session: s, object: object, ctx: ctx, cancel: cancel, primary: primary, done: make(chan struct{})}
	s.dataLeases[l] = true
	return l, nil
}
func (s *Session) DataDocumentCapabilities() contract.Capabilities {
	c := s.Capabilities()
	c.Count = true
	c.EditableTables = true
	return c
}
func (l *dataLease) Runtime() contract.DataRuntime {
	c := l.session.DataDocumentCapabilities()
	c.EditableTables = l.object.Kind == "table"
	r := contract.DataRuntime{Capabilities: c, PageFailureRequiresReopen: true,
		AdvancedLoader: l.page, Loader: l.load, Count: l.count, TableMetadata: l.metadata, TableInfo: l.info,
		FilterModes:   []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"},
		ExportFormats: []string{}, EditKinds: []string{"insert"}, PreviewEditKinds: []string{"insert"},
		EditValueKinds: []contract.CellKind{contract.Null, contract.Text, contract.Integer, contract.Decimal, contract.Floating, contract.Boolean, contract.Binary, contract.DateTime, contract.JSON},
		NativeReceipts: true, SequentialWrites: true, InsertOmittedValues: true,
		MutationPresentation: &contract.DataMutationPresentation{Title: "确认 PrestoSQL 数据新增", Hint: "各 INSERT 语句独立执行，不提供批次事务。省略字段由原生连接器处理；显式 NULL 与省略不同。当前驱动不提供主键，已有行保持只读。未知或不完整回执保留草稿并禁止重放。", PreviewLabel: "PrestoSQL 原生 INSERT 完整预览", HistoryLabel: "已确认的 PrestoSQL 数据新增", CreateLabel: "新增记录"},
		ReadScopeHint:        "PrestoSQL 分页读取当前可见数据；跨页不提供一致快照。",
		ReceiptPresentation:  &contract.NativeWriteReceiptPresentation{ExportFilename: "prestosql-write-receipt.json"},
	}
	if c.EditableTables {
		r.PreviewEdits = l.previewEdits
		r.SubmitEdits = l.submitEdits
	}
	return r
}
func (l *dataLease) identity(id int64, db, table string) error {
	if id != l.object.ProfileID || db != namespaceLabel(l.object.Namespace()) || table != l.object.Name {
		return connections.ErrInvalid
	}
	return l.session.checkNamespace(l.object.Namespace())
}
func (l *dataLease) request(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, connections.ErrInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, contract.ErrRevoked
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if l.active >= 8 {
		return nil, nil, contract.ErrSQLSessionLimit
	}
	l.work.Add(1)
	l.active++
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	return child, func() {
		stop()
		cancel()
		l.mu.Lock()
		l.active--
		l.mu.Unlock()
		l.work.Done()
	}, nil
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
	l.primary.Release()
	l.work.Wait()
	l.mu.Lock()
	if l.plan != nil {
		l.plan.releaseLocked()
		l.plan = nil
	}
	l.unresolved = nil
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
	child, done, err := l.request(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	m, err := l.session.TableMetadata(child, l.object)
	if err == nil {
		m.Editable = l.object.Kind == "table"
		for i := range m.Columns {
			if t, e := parseInsertType(m.Columns[i].Type); e == nil {
				m.Columns[i].ValueKind = t.kind
			}
		}
	}
	return m, err
}
func (l *dataLease) info(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
	m, err := l.metadata(ctx, id, db, table)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: table, Kind: m.Kind, Engine: m.Engine, Comment: m.Comment}, nil
}
func qualified(o contract.ObjectRef) string {
	return quoteName(o.Catalog) + "." + quoteName(o.Schema) + "." + quoteName(o.Name)
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

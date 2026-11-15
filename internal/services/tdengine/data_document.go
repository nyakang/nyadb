package tdengine

import (
	"context"
	"crypto/rand"
	"io"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/services/connections"
)

// One original document owns its key grants and independently canceled native
// read operations. Reservation never opens a socket or performs metadata I/O.
type dataLease struct {
	mu                     sync.Mutex
	session                *Session
	object                 contract.ObjectRef
	ctx                    context.Context
	cancel                 context.CancelFunc
	closed                 bool
	done                   chan struct{}
	work                   sync.WaitGroup
	active                 int
	key                    [32]byte
	meta                   *dataMetadata
	epoch                  uint64
	planning, writeBlocked bool
	writePlan              *dataWritePlan
}

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if err := s.checkObject(o); err != nil {
		return nil, err
	}
	if o.Kind != "table" && o.Kind != "stable" {
		return nil, contract.ErrUnsupported
	}
	l := &dataLease{session: s, object: o, done: make(chan struct{})}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		l.cancel()
		return nil, contract.ErrRevoked
	}
	if len(s.dataLeases) >= 32 {
		l.cancel()
		return nil, contract.ErrSQLSessionLimit
	}
	if s.dataLeases == nil {
		s.dataLeases = map[*dataLease]bool{}
	}
	s.dataLeases[l] = true
	return l, nil
}

func (s *Session) DataDocumentCapabilities() contract.Capabilities {
	c := s.Capabilities()
	// Native grid writes are a separate acceptance step. These original read
	// leases expose complete rows/chunks without advertising a saver.
	c.EditableTables = false
	c.FieldChunks = true
	return c
}

func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{PageCancellationAcknowledged: true, Capabilities: l.session.DataDocumentCapabilities(),
		AdvancedLoader: l.page, RowReader: l.row, Chunk: l.chunk, Count: l.count,
		TableMetadata: l.metadata, TableInfo: l.info, Exporter: l.export, JSONL: l.jsonl,
		FilterModes:        []string{"equal", "not_equal", "less", "greater", "like", "is_null", "is_not_null"},
		ReadScopeHint:      "TDengine 分页读取当前可见数据；跨页不提供一致快照。",
		FieldReadScopeHint: "查看片段会读取完整字段并检查是否发生变化；每次显示最多 1024 字符/字节。"}
}

func (l *dataLease) identity(id int64, db, table string) error {
	if id != l.object.ProfileID || db != l.object.Catalog || table != l.object.Name {
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
	if l.active >= 8 {
		l.mu.Unlock()
		return nil, nil, contract.ErrSQLSessionLimit
	}
	l.active++
	l.work.Add(1)
	l.mu.Unlock()
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	finish := func() {
		stop()
		cancel()
		l.mu.Lock()
		l.active--
		l.mu.Unlock()
		l.work.Done()
	}
	child, parentDone, err := l.session.request(child)
	if err != nil {
		finish()
		return nil, nil, err
	}
	return child, func() { parentDone(); finish() }, nil
}

func (l *dataLease) withWire(ctx context.Context, work func(context.Context, *wire) error) error {
	child, done, err := l.request(ctx)
	if err != nil {
		return err
	}
	defer done()
	select {
	case l.session.metadataSlots <- struct{}{}:
	case <-child.Done():
		return child.Err()
	}
	defer func() { <-l.session.metadataSlots }()
	if err := child.Err(); err != nil {
		return err
	}
	w, err := l.session.connect(child)
	if err != nil {
		return safeError(err, l.session.secret())
	}
	defer w.close()
	return safeError(work(child, w), l.session.secret())
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
	l.meta = nil
	if l.writePlan != nil {
		l.writePlan.releaseLocked()
		l.writePlan = nil
	}
	clear(l.key[:])
	l.mu.Unlock()
	l.session.mu.Lock()
	delete(l.session.dataLeases, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}

func (l *dataLease) info(ctx context.Context, id int64, db, table string) (*contract.TableInfo, error) {
	t, err := l.metadata(ctx, id, db, table)
	if err != nil {
		return nil, err
	}
	return &contract.TableInfo{Database: db, Name: table, Kind: t.Kind, Engine: "TDengine"}, nil
}
func (l *dataLease) jsonl(ctx context.Context, o contract.ObjectRef, writer io.Writer) error {
	if o != l.object {
		return connections.ErrInvalid
	}
	if writer == nil {
		return connections.ErrInvalid
	}
	return l.exportJSONL(ctx, writer)
}

var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

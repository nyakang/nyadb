package consul

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

var errDataReadLost = errors.New("original Consul data observation lost; explicitly reopen the data document")

const (
	dataPageScope = iota
	dataCountScope
	dataFieldScope
	dataExportScope
)

const dataValueBudget = 32 << 20

// Each logical data document owns four independently canceled original HTTP
// executors. No executor is replaced on failure and reservation performs no I/O.
type dataLease struct {
	session          *Session
	object           contract.ObjectRef
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	pageMu           sync.Mutex
	writeMu          sync.Mutex
	work             sync.WaitGroup
	closed, pageLost bool
	epoch            uint64
	key              [32]byte
	scopes           [4]*documentLease
	write            *documentLease
	plan             *dataPlan
	prefixSet        *dataKeyPrefixDeletionSet
	uncertain        bool
	done             chan struct{}
}

func dataCapabilities() contract.Capabilities {
	return contract.Capabilities{KeyValues: true, Metadata: true, EditableTables: true, Keyset: true, Count: true, FieldChunks: true, Export: true}
}

func (s *Session) DataDocumentCapabilities() contract.Capabilities { return dataCapabilities() }

func (s *Session) ReserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	return s.reserveDataDocument(o)
}

func (s *Session) reserveDataDocument(o contract.ObjectRef) (contract.DataDocumentLease, error) {
	if o != s.KVScope().Namespace.Object("keyspace", "keys") {
		return nil, contract.ErrUnsupported
	}
	l := &dataLease{session: s, object: o, epoch: 1, done: make(chan struct{})}
	if _, err := rand.Read(l.key[:]); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.revoked || !s.root.Alive() {
		return nil, contract.ErrRevoked
	}
	if len(s.leases)+len(s.dataLeases) >= 32 {
		return nil, contract.ErrKVLeaseLimit
	}
	l.ctx, l.cancel = context.WithCancel(s.ctx)
	for i := range l.scopes {
		ctx, cancel := context.WithCancel(l.ctx)
		l.scopes[i] = &documentLease{s: s, ctx: ctx, cancel: cancel}
	}
	ctx, cancel := context.WithCancel(l.ctx)
	l.write = &documentLease{s: s, ctx: ctx, cancel: cancel}
	if s.dataLeases == nil {
		s.dataLeases = map[*dataLease]bool{}
	}
	s.dataLeases[l] = true
	return l, nil
}

func (l *dataLease) Runtime() contract.DataRuntime {
	return contract.DataRuntime{Capabilities: dataCapabilities(),
		PageFailureRequiresReopen: true, AdvancedLoader: l.page, Count: l.count, RowReader: l.row, Chunk: l.chunk,
		Exporter: l.export, TableMetadata: l.metadata, TableInfo: l.info,
		Saver: l.save, PreviewEdits: l.preview, PreviewKeyCopy: l.previewKeyCopy, PreviewKeyPrefixDelete: l.previewKeyPrefixDelete, CaptureKeyPrefixDelete: l.captureKeyPrefixDelete, SubmitEdits: l.submit,
		NativeReceipts: true, InsertOmittedValues: true,
		EditKinds: []string{"insert", "update", "delete", "copy"}, PreviewEditKinds: []string{"insert", "update", "delete", "copy"},
		EditValueKinds:       []contract.CellKind{contract.Text, contract.Binary, contract.Integer},
		MutationPresentation: &contract.DataMutationPresentation{Title: "确认键更改", Hint: "请检查原始键、目标键、完整值和 Flags；确认后将原子提交此批次更改。", PreviewLabel: "键更改完整预览", HistoryLabel: "已确认的原生键更改", CreateLabel: "创建键", UpdateLabel: "更新键", DeleteLabel: "删除键", CopyLabel: "复制键"},
		WriteDescription:     "PUT /v1/txn (one original atomic CAS; server transaction limit applies)",
		ExportFormats:        []string{"csv", "json"},
		FilterColumns:        []string{}, SortColumns: []string{}, FilterModes: []string{},
		ReadScopeHint: "原前缀内可见键的有界实时观察；翻页、统计和导出不共享固定快照。",
		ReadDescription: func(contract.DataOptions) string {
			return "Consul KV recurse/get-tree (one bounded original live HTTP observation)"
		},
	}
}

func (l *dataLease) check(id int64, catalog, name string) error {
	if id != l.object.ProfileID || catalog != l.object.Catalog || name != l.object.Name {
		return ErrInvalid
	}
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || !l.session.Alive() {
		return contract.ErrRevoked
	}
	return nil
}

func (l *dataLease) checkPage(id int64, catalog, name string) error {
	if err := l.check(id, catalog, name); err != nil {
		return err
	}
	l.mu.Lock()
	lost := l.pageLost
	l.mu.Unlock()
	if lost {
		return errDataReadLost
	}
	return nil
}

func (l *dataLease) begin(parent context.Context) (context.Context, func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || !l.session.Alive() {
		return nil, nil, contract.ErrRevoked
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	l.work.Add(1)
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(l.ctx, cancel)
	return ctx, func() { stop(); cancel(); l.work.Done() }, nil
}

func (l *dataLease) read(ctx context.Context, scope int, request Request) (contract.KVResult, error) {
	if err := l.check(l.object.ProfileID, l.object.Catalog, l.object.Name); err != nil {
		return contract.KVResult{}, err
	}
	r, err := DocumentRequest(request)
	if err != nil {
		return contract.KVResult{}, err
	}
	p, err := l.scopes[scope].Prepare(r)
	if err != nil {
		return contract.KVResult{}, err
	}
	if p.Writes() {
		return contract.KVResult{}, contract.ErrUnsupported
	}
	return l.scopes[scope].Execute(ctx, p)
}

func (l *dataLease) encode(value any) []byte {
	raw, _ := json.Marshal(value)
	l.mu.Lock()
	key := l.key
	l.mu.Unlock()
	mac := hmac.New(sha256.New, key[:])
	mac.Write(raw)
	envelope, _ := json.Marshal(dataEnvelope{Body: raw, MAC: hex.EncodeToString(mac.Sum(nil))})
	return envelope
}

type dataEnvelope struct {
	Body json.RawMessage `json:"body"`
	MAC  string          `json:"mac"`
}

func (l *dataLease) decode(raw []byte, value any) error {
	var envelope dataEnvelope
	if jsondocument.Decode(raw, &envelope, 64<<10) != nil || envelope.Body == nil {
		return ErrInvalid
	}
	got, err := hex.DecodeString(envelope.MAC)
	if err != nil {
		return ErrInvalid
	}
	l.mu.Lock()
	key, closed := l.key, l.closed
	l.mu.Unlock()
	mac := hmac.New(sha256.New, key[:])
	mac.Write(envelope.Body)
	if closed || !hmac.Equal(got, mac.Sum(nil)) || jsondocument.Decode(envelope.Body, value, 64<<10) != nil {
		return ErrInvalid
	}
	return nil
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
	if l.prefixSet != nil {
		l.prefixSet.closeLocked()
	}
	if l.plan != nil {
		l.plan.releaseLocked()
	}
	l.mu.Unlock()
	for _, scope := range l.scopes {
		scope.Close()
	}
	l.work.Wait()
	// Mutation workers classify and retain their original receipt after the
	// executor returns. Join that classification before clearing its caches.
	l.write.Close()
	l.mu.Lock()
	clear(l.key[:])
	l.mu.Unlock()
	l.session.mu.Lock()
	delete(l.session.dataLeases, l)
	l.session.mu.Unlock()
	close(l.done)
	return nil
}

var _ contract.DataDocumentLease = (*dataLease)(nil)
var _ contract.DataDocumentsProvider = (*Session)(nil)
var _ contract.DataDocumentFeaturesProvider = (*Session)(nil)

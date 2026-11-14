package consul

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type httpArchivePreparation struct {
	owner     *documentLease
	authority *httpArchivePreparation
	request   contract.KVHTTPArchiveRequest
	scope     contract.KVHTTPArchiveScope
	target    string
	record    string
	used      atomic.Bool
}

func (p *httpArchivePreparation) Snapshot() contract.KVHTTPArchiveRequest { return p.request }
func (p *httpArchivePreparation) Scope() contract.KVHTTPArchiveScope      { return p.scope.Clone() }
func (p *httpArchivePreparation) TargetURL() string                       { return p.target }
func (p *httpArchivePreparation) LocalRecordID() string                   { return p.record }

func httpArchiveLimits(r contract.KVHTTPArchiveRequest) (snapshotArchiveLimits, error) {
	if r.CompressedBytes == 0 || r.CompressedBytes > 64<<20 || r.ExpandedBytes == 0 || r.ExpandedBytes > 256<<20 {
		return snapshotArchiveLimits{}, ErrInvalid
	}
	return snapshotArchiveLimits{compressed: int64(r.CompressedBytes), expanded: int64(r.ExpandedBytes)}, nil
}

func (l *documentLease) PrepareHTTPArchive(request contract.KVHTTPArchiveRequest) (contract.KVHTTPArchivePreparation, error) {
	if err := l.CheckKVLease(); err != nil {
		return nil, err
	}
	if _, err := httpArchiveLimits(request); err != nil {
		return nil, err
	}
	target, err := snapshotArchiveTarget(l.s.cfg)
	if err != nil {
		return nil, err
	}
	p := &httpArchivePreparation{owner: l, request: request, scope: contract.KVHTTPArchiveScope{Original: l.Scope(), DocumentID: l.document}, target: target, record: rand.Text()}
	p.authority = p
	return p, nil
}

// A caller-owned archive keeps its budget after physical retirement. Recovery
// moves this shared account to the destination, including archives still held
// by the original document. All account fields use receiptBudgetMu.
type httpArchiveBudget struct {
	s        *Session
	retained int64
}

// The caller holds receiptBudgetMu.
func (l *documentLease) archiveBytes() int64 {
	if l.archiveBudget == nil {
		return 0
	}
	return l.archiveBudget.retained
}

// Independent artifacts are not duplicated in LastResult or RecoverKV.
type httpArchive struct {
	mu      sync.Mutex
	budget  *httpArchiveBudget
	result  contract.KVHTTPArchiveResult
	body    []byte
	charged int64
	closed  bool
}

func (a *httpArchive) Receipt() contract.KVHTTPArchiveResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result.Clone()
}

func (a *httpArchive) OpenOriginal() (io.ReadCloser, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, contract.ErrRevoked
	}
	return &httpArchiveReader{archive: a}, nil
}

func (a *httpArchive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	clear(a.body)
	clear(a.result.Metadata)
	clear(a.result.Checksums)
	a.body, a.result = nil, contract.KVHTTPArchiveResult{}
	receiptBudgetMu.Lock()
	a.budget.s.retained -= a.charged
	a.budget.retained -= a.charged
	receiptBudgetMu.Unlock()
	a.charged, a.budget = 0, nil
	return nil
}

type httpArchiveReader struct {
	archive *httpArchive
	offset  int
	closed  bool // guarded by archive.mu, like offset
}

func (r *httpArchiveReader) Read(p []byte) (int, error) {
	a := r.archive
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || r.closed {
		return 0, contract.ErrRevoked
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset == len(a.body) {
		return 0, io.EOF
	}
	n := copy(p, a.body[r.offset:])
	r.offset += n
	return n, nil
}

func (r *httpArchiveReader) Close() error {
	r.archive.mu.Lock()
	r.closed = true
	r.archive.mu.Unlock()
	return nil
}

func (l *documentLease) reserveHTTPArchive(request contract.KVHTTPArchiveRequest) (int64, error) {
	// Account for bounded io.ReadAll growth, output scratch and metadata, plus
	// the original preconnect evidence before any request is sent.
	charge := max(pendingReceiptBudget, 4*int64(request.CompressedBytes)+(4<<20))
	receiptBudgetMu.Lock()
	defer receiptBudgetMu.Unlock()
	if l.cacheBytes+l.planReserved+l.archiveBytes()+charge > documentReceiptBudget || l.s.retained+l.s.reserved+charge > l.s.budget {
		return 0, ErrBounds
	}
	if l.archiveBudget == nil {
		l.archiveBudget = &httpArchiveBudget{s: l.s}
	}
	l.s.reserved += charge
	return charge, nil
}

func (l *documentLease) CaptureHTTPArchive(parent context.Context, prepared contract.KVHTTPArchivePreparation) (artifact contract.KVHTTPArchive, out contract.KVHTTPArchiveResult, err error) {
	p, ok := prepared.(*httpArchivePreparation)
	if !ok || p == nil || p.owner != l || p.authority != p {
		return nil, out, contract.ErrKVPlanUsed
	}
	out = contract.KVHTTPArchiveResult{Scope: p.Scope(), Request: p.request, TargetURL: p.target, LocalRecordID: p.record}
	if !l.ops.TryLock() {
		return nil, out, contract.ErrKVBusy
	}
	defer l.ops.Unlock()
	if p.used.Load() {
		return nil, out, contract.ErrKVPlanUsed
	}
	ctx, done, err := l.request(parent)
	if err != nil {
		return nil, out, err
	}
	defer done()
	charge, err := l.reserveHTTPArchive(p.request)
	if err != nil {
		return nil, out, err
	}
	defer func() {
		receiptBudgetMu.Lock()
		l.s.reserved -= charge
		receiptBudgetMu.Unlock()
	}()
	if !p.used.CompareAndSwap(false, true) {
		return nil, out, contract.ErrKVPlanUsed
	}
	defer func() {
		out.Ended = true
		out.Canceled = out.Canceled || ctx.Err() != nil
		if err != nil {
			out.LocalError = httpArchiveErrorText(err)
		}
		if a, ok := artifact.(*httpArchive); ok {
			a.result = out.Clone()
		}
	}()
	if err = l.connect(ctx); err != nil {
		return nil, out, err
	}
	limits, _ := httpArchiveLimits(p.request)
	native, err := l.client.prepareSnapshotArchive(limits)
	if err != nil {
		return nil, out, err
	}
	if native.target != p.target {
		return nil, out, contract.ErrRevoked
	}
	captured, err := l.client.captureSnapshotArchive(ctx, native, io.Discard)
	if !l.client.Alive() {
		err = l.markLost(err)
	}
	out = projectHTTPArchive(p, captured, err)
	if !captured.attempted {
		captured.clear()
		return nil, out, err
	}
	// Transfer ownership of the original bytes; only small receipt metadata is
	// copied. A failed capture still retains its complete observed error/prefix.
	a := &httpArchive{result: out.Clone(), body: captured.body}
	a.charged = int64(len(a.body)+len(a.result.Metadata)+len(a.result.Checksums)) + resultCost(&contract.KVResult{HTTP: &contract.KVHTTPResult{Header: captured.header, Trailer: captured.trailer}}) + 64<<10
	receiptBudgetMu.Lock()
	a.budget = l.archiveBudget
	l.s.retained += a.charged
	a.budget.retained += a.charged
	receiptBudgetMu.Unlock()
	captured.body = nil
	captured.clear()
	return a, out, err
}

func projectHTTPArchive(p *httpArchivePreparation, r snapshotArchiveCapture, err error) contract.KVHTTPArchiveResult {
	hash := sha256.Sum256(r.body)
	proof := r.proof
	return contract.KVHTTPArchiveResult{Scope: p.Scope(), Request: p.request, LocalRecordID: p.record, TargetURL: r.target, Protocol: r.protocol, Status: r.status,
		Header: r.header.Clone(), Trailer: r.trailer.Clone(), Attempted: r.attempted, Ended: r.ended, EOF: r.eof, HTTPComplete: r.httpComplete, SourceComplete: r.sourceComplete, Canceled: r.canceled,
		GZIPComplete: proof.gzipComplete, TARComplete: proof.tarComplete, ChecksumsMatch: proof.checksumsMatch, MetadataValid: proof.metadataValid,
		OriginalBytes: uint64(len(r.body)), ExpandedBytes: uint64(proof.expandedBytes), StateBytes: uint64(proof.stateBytes), OriginalSHA256: hash[:], StateSHA256: append([]byte(nil), proof.stateHash[:]...),
		Metadata: append([]byte(nil), proof.metadata...), Checksums: append([]byte(nil), proof.checksums...), Version: proof.version, Index: proof.index, Term: proof.term, SnapshotID: proof.id, LocalError: httpArchiveErrorText(err)}
}

func httpArchiveErrorText(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "original HTTP archive canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "original HTTP archive deadline exceeded"
	case errors.Is(err, ErrBounds):
		return "original HTTP archive exceeds bounds"
	case errors.Is(err, ErrProtocol):
		return "original HTTP archive validation failed"
	}
	var native *HTTPError
	if errors.As(err, &native) {
		return native.Error()
	}
	return "original HTTP archive capture failed"
}

var _ contract.KVHTTPArchiveCapturing = (*documentLease)(nil)
var _ contract.KVHTTPArchive = (*httpArchive)(nil)

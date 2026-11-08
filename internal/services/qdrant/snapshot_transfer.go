package qdrant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits apply to archive bytes, independently of the bounded JSON transport.
// A zero timeout uses the profile request timeout. There is no range/resume or
// automatic retry. Callers explicitly choose a bound, up to MaxInt64 bytes.
type SnapshotTransferOptions struct {
	MaxBytes uint64
	Timeout  time.Duration
}

func (o SnapshotTransferOptions) validate() error {
	if o.MaxBytes == 0 || o.MaxBytes > math.MaxInt64 || o.Timeout < 0 {
		return ErrInvalid
	}
	return nil
}

type SnapshotTransfer struct {
	Target                          SnapshotTarget
	Name, Path                      string
	Response                        Response
	ExpectedSize, Received, Written uint64
	ExpectedChecksum, SHA256        string
	ContentLength                   int64
	Prefix                          []byte
	Complete                        bool
}

func cloneSnapshotTransfer(r SnapshotTransfer) SnapshotTransfer {
	r.Response.Body = bytes.Clone(r.Response.Body)
	r.Prefix = bytes.Clone(r.Prefix)
	return r
}

// beginTransfer shares the original client's operation/close discipline. The
// owned source/destination is closed on cancellation to acknowledge blocking IO.
func (c *Client) beginTransfer(ctx context.Context, r Request, o SnapshotTransferOptions, closeOwned func() error) (context.Context, func(), error) {
	if err := o.validate(); err != nil {
		return nil, nil, err
	}
	checked := r
	checked.ContentType = "application/json"
	checked.Body = nil
	if err := validateRequest(checked); err != nil {
		return nil, nil, err
	}
	if !c.operation.TryLock() {
		return nil, nil, ErrBusy
	}
	c.mu.Lock()
	unavailable := c.closed || c.retired
	c.mu.Unlock()
	if unavailable {
		c.operation.Unlock()
		return nil, nil, ErrRetired
	}
	if err := ctx.Err(); err != nil {
		c.operation.Unlock()
		return nil, nil, err
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = time.Duration(c.config.RequestTimeoutSeconds) * time.Second
	}
	child, cancel := context.WithTimeout(ctx, timeout)
	stopRoot := context.AfterFunc(c.ctx, cancel)
	stopIO := context.AfterFunc(child, func() { _ = closeOwned() })
	done := func() {
		if !stopIO() {
			// onceClose waits for a cancellation callback already closing the
			// owned stream before another operation or Client.Close can settle.
			_ = closeOwned()
		}
		stopRoot()
		cancel()
		c.operation.Unlock()
	}
	return child, done, nil
}
func (c *Client) transferRequest(ctx context.Context, r Request, body io.Reader, length int64) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, c.config.Endpoint+r.Path, body)
	if err != nil {
		return nil, ErrInvalid
	}
	req.GetBody = nil
	req.ContentLength = length
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Accept", "application/octet-stream")
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	c.mu.Lock()
	secret := c.secret
	c.mu.Unlock()
	if c.config.Auth == "api_key" {
		req.Header.Set("api-key", secret)
	}
	return req, nil
}
func onceClose(closer io.Closer) func() error {
	var once sync.Once
	var err error
	return func() error { once.Do(func() { err = closer.Close() }); return err }
}
func (c *Client) transferError(ctx context.Context, result *SnapshotTransfer, cause error) error {
	result.Complete = false
	result.Response.Incomplete = true
	c.retire()
	if ctx.Err() != nil {
		cause = ctx.Err()
	}
	return &Interrupted{WriteOutcomeUnknown: false, Cause: cause}
}

// DownloadSnapshot owns and closes the destination. It re-reads the original
// selected metadata before dispatch and checks length and any native SHA256.
// Descriptions do not provide a remote incarnation or atomic read lock.
func (c *Client) DownloadSnapshot(ctx context.Context, target SnapshotTarget, selected SnapshotDescription, sink io.WriteCloser, o SnapshotTransferOptions) (SnapshotTransfer, error) {
	result := SnapshotTransfer{Target: target, Name: selected.Name, ExpectedSize: selected.Size, ExpectedChecksum: selected.Checksum, ContentLength: -1}
	if sink == nil {
		return result, ErrInvalid
	}
	closeSink := onceClose(sink)
	defer closeSink()
	if err := o.validate(); err != nil {
		return result, err
	}
	if err := validateSnapshotDescription(selected); err != nil {
		return result, err
	}
	if selected.Size > o.MaxBytes {
		return result, ErrBounds
	}
	path, err := target.path()
	if err != nil {
		return result, err
	}
	name, _ := snapshotName(selected.Name)
	result.Path = path + "/" + name
	// Every expected field is copied; caller Raw bytes never authorize a request.
	selected = cloneSnapshotDescription(selected)
	listed, _, err := c.ListSnapshots(ctx, target)
	if err != nil {
		return result, err
	}
	found := false
	for _, current := range listed {
		if current.Name == selected.Name {
			found = current.Size == selected.Size && current.HasCreationTime == selected.HasCreationTime && current.CreationTime == selected.CreationTime && strings.EqualFold(current.Checksum, selected.Checksum)
		}
	}
	if !found {
		return result, ErrSnapshotChanged
	}
	return c.downloadSnapshot(ctx, result, sink, closeSink, o, true)
}

// StreamShardSnapshot downloads the current shard state directly. It has no
// immutable filename, listed size or listed checksum to invent or compare.
func (c *Client) StreamShardSnapshot(ctx context.Context, target SnapshotTarget, sink io.WriteCloser, o SnapshotTransferOptions) (SnapshotTransfer, error) {
	result := SnapshotTransfer{Target: target, ContentLength: -1}
	if sink == nil {
		return result, ErrInvalid
	}
	closeSink := onceClose(sink)
	defer closeSink()
	path, err := target.path()
	if err != nil || target.Scope != ShardSnapshots {
		return result, ErrInvalid
	}
	result.Path = strings.TrimSuffix(path, "snapshots") + "snapshot"
	return c.downloadSnapshot(ctx, result, sink, closeSink, o, false)
}
func (c *Client) downloadSnapshot(ctx context.Context, result SnapshotTransfer, sink io.WriteCloser, closeSink func() error, o SnapshotTransferOptions, listed bool) (SnapshotTransfer, error) {
	r := Request{Method: "GET", Path: result.Path}
	child, done, err := c.beginTransfer(ctx, r, o, closeSink)
	if err != nil {
		return result, err
	}
	defer done()
	req, err := c.transferRequest(child, r, nil, 0)
	if err != nil {
		return result, err
	}
	response, err := c.http.Do(req)
	if err != nil {
		err = c.transferError(child, &result, errors.New("Qdrant snapshot HTTP transport failed"))
		return result, err
	}
	defer response.Body.Close()
	result.Response.Status, result.Response.ContentType = response.StatusCode, response.Header.Get("Content-Type")
	result.ContentLength = response.ContentLength
	if response.StatusCode != http.StatusOK {
		result.Response.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
		if len(result.Response.Body) > responseLimit {
			result.Response.Body = result.Response.Body[:responseLimit]
			err = ErrBounds
		}
		if err != nil {
			err = c.transferError(child, &result, err)
			return result, err
		}
		return result, &HTTPError{Status: response.StatusCode}
	}
	media, _, err := mime.ParseMediaType(result.Response.ContentType)
	if err != nil || media != "application/octet-stream" || response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		err = c.transferError(child, &result, ErrProtocol)
		return result, err
	}
	if response.ContentLength >= 0 && (uint64(response.ContentLength) > o.MaxBytes || listed && uint64(response.ContentLength) != result.ExpectedSize) {
		err = c.transferError(child, &result, ErrSnapshotChanged)
		return result, err
	}
	digest := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err = child.Err(); err != nil {
			break
		}
		// One extra byte detects oversized chunked bodies without writing beyond the bound.
		capacity := uint64(len(buffer))
		remaining := o.MaxBytes - result.Received
		if capacity > remaining+1 {
			capacity = remaining + 1
		}
		n, readErr := response.Body.Read(buffer[:int(capacity)])
		if n > 0 {
			result.Received += uint64(n)
			_, _ = digest.Write(buffer[:n])
			if len(result.Prefix) < 1024 {
				keep := min(n, 1024-len(result.Prefix))
				result.Prefix = append(result.Prefix, buffer[:keep]...)
			}
			if result.Received > o.MaxBytes || listed && result.Received > result.ExpectedSize {
				err = ErrBounds
				break
			}
			written, writeErr := sink.Write(buffer[:n])
			if written < 0 || written > n {
				err = ErrSnapshotSink
				break
			}
			result.Written += uint64(written)
			if writeErr != nil {
				err = errors.Join(ErrSnapshotSink, writeErr)
				break
			}
			if written != n {
				err = errors.Join(ErrSnapshotSink, io.ErrShortWrite)
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				err = readErr
			}
			break
		}
	}
	result.SHA256 = hex.EncodeToString(digest.Sum(nil))
	if err == nil && listed && result.Received != result.ExpectedSize {
		err = ErrSnapshotChanged
	}
	if err == nil && result.ExpectedChecksum != "" && !strings.EqualFold(result.SHA256, result.ExpectedChecksum) {
		err = ErrSnapshotChecksum
	}
	if err == nil {
		if closeErr := closeSink(); closeErr != nil {
			err = errors.Join(ErrSnapshotSink, closeErr)
		}
	}
	if err == nil {
		err = child.Err()
	}
	if err != nil {
		err = c.transferError(child, &result, err)
		return result, err
	}
	result.Complete = true
	return cloneSnapshotTransfer(result), nil
}

type snapshotUploadPlan struct {
	name, checksum string
	size           uint64
	header, footer []byte
}

// Upload preparation captures metadata and exact native multipart framing,
// without opening, buffering, hashing or transmitting the selected archive.
func PrepareSnapshotUpload(target SnapshotTarget, name string, size uint64, checksum, priority string, wait bool) (SnapshotPlan, error) {
	path, err := target.path()
	if err != nil || target.Scope == StorageSnapshots {
		return SnapshotPlan{}, ErrInvalid
	}
	if _, err = snapshotName(name); err != nil || size > math.MaxInt64-4096 || checksum != "" && !validSnapshotChecksum(checksum) {
		return SnapshotPlan{}, ErrInvalid
	}
	if priority != "" && priority != "no_sync" && priority != "snapshot" && priority != "replica" {
		return SnapshotPlan{}, ErrInvalid
	}
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	if _, err = writer.CreateFormFile("snapshot", name); err != nil {
		return SnapshotPlan{}, ErrInvalid
	}
	header := bytes.Clone(framing.Bytes())
	framing.Reset()
	if err = writer.Close(); err != nil {
		return SnapshotPlan{}, ErrInvalid
	}
	footer := bytes.Clone(framing.Bytes())
	query := url.Values{"wait": {strconv.FormatBool(wait)}}
	if priority != "" {
		query.Set("priority", priority)
	}
	if checksum != "" {
		query.Set("checksum", checksum)
	}
	r := Request{Method: "POST", Path: path + "/upload?" + query.Encode(), ContentType: writer.FormDataContentType()}
	return SnapshotPlan{action: "upload", target: target, request: r, preview: r, used: &atomic.Bool{}, upload: &snapshotUploadPlan{name: name, size: size, checksum: checksum, header: header, footer: footer}}, nil
}

// archiveReader checks exact input length and optional SHA256 before exposing
// the terminating multipart boundary. Already transmitted bytes cannot rollback.
type archiveReader struct {
	source         io.Reader
	expected, read uint64
	checksum       string
	digest         hash.Hash
	checked        bool
}

func (r *archiveReader) Read(buffer []byte) (int, error) {
	if r.checked {
		return 0, io.EOF
	}
	if r.read == r.expected {
		var extra [1]byte
		n, err := r.source.Read(extra[:])
		if n != 0 {
			return 0, ErrSnapshotSource
		}
		if err == nil {
			return 0, nil
		}
		if !errors.Is(err, io.EOF) {
			return 0, err
		}
		if r.checksum != "" && !strings.EqualFold(hex.EncodeToString(r.digest.Sum(nil)), r.checksum) {
			return 0, ErrSnapshotChecksum
		}
		r.checked = true
		return 0, io.EOF
	}
	if uint64(len(buffer)) > r.expected-r.read {
		buffer = buffer[:int(r.expected-r.read)]
	}
	n, err := r.source.Read(buffer)
	r.read += uint64(n)
	if n > 0 {
		_, _ = r.digest.Write(buffer[:n])
	}
	if errors.Is(err, io.EOF) {
		if r.read != r.expected {
			return n, ErrSnapshotSource
		}
		err = nil
	}
	return n, err
}

// Close interrupts the owned source and waits for the original body reader.
// net/http may return an early response before its upload writer has finished.
// Only after this acknowledgement may its byte counters/hash be consumed.
type snapshotUploadBody struct {
	mu          sync.Mutex
	closed      atomic.Bool
	reader      io.Reader
	closeSource func() error
	readErr     error
}

func (b *snapshotUploadBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := b.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		b.readErr = err
	}
	return n, err
}
func (b *snapshotUploadBody) Close() error {
	b.closed.Store(true)
	err := b.closeSource()
	b.mu.Lock()
	b.mu.Unlock()
	return err
}

func (c *Client) SubmitSnapshotUpload(ctx context.Context, p SnapshotPlan, source io.ReadCloser, o SnapshotTransferOptions) (SnapshotReceipt, error) {
	receipt := SnapshotReceipt{Action: p.action, Target: p.target, State: "not_submitted"}
	if source == nil {
		return receipt, ErrInvalid
	}
	closeSource := onceClose(source)
	defer closeSource()
	if p.used == nil || p.action != "upload" || p.upload == nil {
		return receipt, ErrInvalid
	}
	if err := o.validate(); err != nil {
		return receipt, err
	}
	if p.upload.size > o.MaxBytes {
		return receipt, ErrBounds
	}
	child, done, err := c.beginTransfer(ctx, p.request, o, closeSource)
	if err != nil {
		return receipt, err
	}
	defer done()
	if !p.used.CompareAndSwap(false, true) {
		return receipt, ErrSnapshotUsed
	}
	archive := &archiveReader{source: source, expected: p.upload.size, checksum: p.upload.checksum, digest: sha256.New()}
	body := &snapshotUploadBody{reader: io.MultiReader(bytes.NewReader(p.upload.header), archive, bytes.NewReader(p.upload.footer)), closeSource: closeSource}
	req, err := c.transferRequest(child, p.request, body, int64(len(p.upload.header))+int64(p.upload.size)+int64(len(p.upload.footer)))
	if err != nil {
		return receipt, err
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	sourceCloseErr := body.Close()
	receipt.SourceBytesRead, receipt.SourceSHA256 = archive.read, hex.EncodeToString(archive.digest.Sum(nil))
	fail := func(cause error) (SnapshotReceipt, error) {
		receipt.State = "uncertain"
		receipt.Response.Incomplete = true
		c.retire()
		if child.Err() != nil {
			cause = child.Err()
		}
		return receipt, &Interrupted{WriteOutcomeUnknown: true, Cause: cause}
	}
	if err != nil {
		return fail(errors.Join(errors.New("Qdrant snapshot upload executor interrupted"), body.readErr))
	}
	defer response.Body.Close()
	receipt.Response.Status, receipt.Response.ContentType = response.StatusCode, response.Header.Get("Content-Type")
	receipt.Response.Body, err = io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if len(receipt.Response.Body) > responseLimit {
		receipt.Response.Body = receipt.Response.Body[:responseLimit]
		err = ErrBounds
	}
	if err != nil {
		return fail(err)
	}
	if child.Err() != nil {
		return fail(child.Err())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fail(&HTTPError{Status: response.StatusCode})
	}
	if !archive.checked || archive.read != p.upload.size {
		return fail(ErrSnapshotSource)
	}
	parsed, err := parseSnapshotReceipt(p, receipt.Response)
	if err != nil {
		return fail(err)
	}
	parsed.SourceBytesRead, parsed.SourceSHA256 = receipt.SourceBytesRead, receipt.SourceSHA256
	if sourceCloseErr != nil {
		return parsed, errors.Join(ErrSnapshotSource, sourceCloseErr)
	}
	return parsed, nil
}

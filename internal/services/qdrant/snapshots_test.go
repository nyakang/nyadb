package qdrant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func snapshotHash(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func snapshotEnvelope(result string) string {
	return `{"status":"ok","time":0.01,"result":` + result + `}`
}
func descriptionJSON(name string, size uint64, checksum string) string {
	return fmt.Sprintf(`{"name":%q,"size":%d,"creation_time":null,"checksum":%q}`, name, size, checksum)
}

var unitSnapshotTarget = SnapshotTarget{Scope: CollectionSnapshots, Collection: "owned"}

type snapshotSink struct {
	digest   hash.Hash
	count    uint64
	closed   atomic.Int32
	closeErr error
	short    bool
}

func newSnapshotSink() *snapshotSink { return &snapshotSink{digest: sha256.New()} }
func (s *snapshotSink) Write(p []byte) (int, error) {
	n := len(p)
	if s.short {
		n /= 2
	}
	s.count += uint64(n)
	_, _ = s.digest.Write(p[:n])
	return n, nil
}
func (s *snapshotSink) Close() error { s.closed.Add(1); return s.closeErr }
func TestSnapshotExactDescriptorsScopesAndNativeRecoverySecrets(t *testing.T) {
	raw := `{"name":"owned.snapshot","size":18446744073709551615,"creation_time":"2026-11-09T06:00:00.123456","checksum":null,"extra":{"n":9007199254740993}}`
	d, err := parseSnapshotDescription([]byte(raw))
	if err != nil || d.Size != ^uint64(0) || string(d.Raw) != raw {
		t.Fatal("description lost precision", err)
	}
	for _, bad := range []string{`{"name":"../s","size":1,"creation_time":null}`, `{"name":"s","size":null,"creation_time":null}`, `{"name":"s","size":1.0,"creation_time":null}`, `{"name":"s","size":-1,"creation_time":null}`, `{"name":"s","size":1,"size":2,"creation_time":null}`, `{"name":"s","size":1,"creation_time":"yesterday"}`, `{"name":"s","size":1,"creation_time":null,"checksum":"bad"}`} {
		if _, err := parseSnapshotDescription([]byte(bad)); err == nil {
			t.Fatal("invalid description accepted", bad)
		}
	}
	for _, target := range []SnapshotTarget{{Scope: StorageSnapshots}, {Scope: CollectionSnapshots, Collection: "原 集合"}, {Scope: ShardSnapshots, Collection: "owned", Shard: ^uint32(0)}} {
		plan, err := PrepareSnapshotCreate(target, false)
		if err != nil || !strings.HasSuffix(plan.Preview().Path, "?wait=false") {
			t.Fatal(err)
		}
	}
	for _, target := range []SnapshotTarget{{Scope: StorageSnapshots, Collection: "owned"}, {Scope: CollectionSnapshots, Collection: "owned", Shard: 1}, {Scope: ShardSnapshots, Collection: "../elsewhere"}, {Scope: "unknown"}} {
		if _, err := PrepareSnapshotCreate(target, true); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	recovery := SnapshotRecovery{Location: "https://snapshot.example/owned.snapshot", Priority: "no_sync", Checksum: strings.Repeat("a", 64), APIKey: "source-private-key"}
	plan, err := PrepareSnapshotRecovery(unitSnapshotTarget, recovery, true)
	if err != nil || bytes.Contains(plan.Preview().Body, []byte(recovery.APIKey)) || !bytes.Contains(plan.request.Body, []byte(recovery.APIKey)) {
		t.Fatal("source secret exposed or lost", err)
	}
	copy := plan.Preview()
	copy.Body[0] = 'x'
	if plan.Preview().Body[0] != '{' {
		t.Fatal("preview changed private request")
	}
	if _, err = PrepareSnapshotRecovery(SnapshotTarget{Scope: StorageSnapshots}, recovery, true); err == nil {
		t.Fatal("invented storage REST restore")
	}
	for _, r := range []SnapshotRecovery{{Location: "https://user:secret@example/s"}, {Location: "https://example/s#fragment"}, {Location: "file://other/s"}, {Location: "/server/s"}, {Location: "https://example/s", Priority: "internal"}, {Location: "https://example/s", Checksum: "short"}} {
		if _, err := PrepareSnapshotRecovery(unitSnapshotTarget, r, true); err == nil {
			t.Fatal("invalid recovery accepted")
		}
	}
	if _, err := PrepareSnapshotRecovery(SnapshotTarget{Scope: ShardSnapshots, Collection: "owned"}, SnapshotRecovery{Location: "snapshots/local.snapshot", Priority: "replica"}, false); err != nil {
		t.Fatal("native shard local path rejected", err)
	}
}

func TestSnapshotUploadShardNullAndBooleanReceiptsStayDistinct(t *testing.T) {
	for _, scope := range []SnapshotScope{CollectionSnapshots, ShardSnapshots} {
		for _, result := range []string{"null", "true", "false", "{}"} {
			p, err := PrepareSnapshotUpload(SnapshotTarget{Scope: scope, Collection: "owned"}, "owned.snapshot", 1, "", "", true)
			if err != nil {
				t.Fatal(err)
			}
			r, err := parseSnapshotReceipt(p, Response{Status: 200, Body: []byte(snapshotEnvelope(result))})
			valid := scope == ShardSnapshots && result == "null" || scope == CollectionSnapshots && (result == "true" || result == "false")
			if (err == nil) != valid {
				t.Fatal("native receipt type was changed", scope, result, err)
			}
			if valid && (r.HasNullResult != (scope == ShardSnapshots) || r.HasResult != (scope == CollectionSnapshots) || r.Result != (result == "true") || r.State != "completed") {
				t.Fatal("native null/false/true receipt lost", scope, result, r)
			}
		}
	}
	p, _ := PrepareSnapshotDelete(unitSnapshotTarget, "owned.snapshot", true)
	if _, err := parseSnapshotReceipt(p, Response{Status: 200, Body: []byte(snapshotEnvelope("null"))}); err == nil {
		t.Fatal("null deletion invented a boolean result")
	}
}
func TestSnapshotAdmissionBooleanFalseOneUseAndOriginalFailureEvidence(t *testing.T) {
	for _, mode := range []string{"accepted", "false", "malformed", "lost"} {
		t.Run(mode, func(t *testing.T) {
			var writes atomic.Int32
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				writes.Add(1)
				switch mode {
				case "accepted":
					w.WriteHeader(202)
					_, _ = io.WriteString(w, `{"status":"accepted","time":0.01}`)
				case "false":
					_, _ = io.WriteString(w, snapshotEnvelope("false"))
				case "malformed":
					_, _ = io.WriteString(w, snapshotEnvelope(`{"name":"missing fields"}`))
				case "lost":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
				}
			})
			plan, _ := PrepareSnapshotCreate(unitSnapshotTarget, true)
			if mode == "false" {
				plan, _ = PrepareSnapshotDelete(unitSnapshotTarget, "owned.snapshot", true)
			}
			copy := plan
			receipt, err := c.SubmitSnapshot(context.Background(), plan)
			if mode == "accepted" && (err != nil || receipt.State != "enqueued" || receipt.Description != nil || receipt.HasResult) {
				t.Fatal("admission invented completion", err, receipt)
			}
			if mode == "false" && (err != nil || receipt.State != "completed" || !receipt.HasResult || receipt.Result) {
				t.Fatal("native false lost", err)
			}
			if mode == "malformed" || mode == "lost" {
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || receipt.State != "uncertain" {
					t.Fatal("uncertainty lost", err, receipt.State)
				}
			}
			if _, err = c.SubmitSnapshot(context.Background(), copy); !errors.Is(err, ErrSnapshotUsed) || writes.Load() != 1 {
				t.Fatal("original snapshot replayed", err)
			}
		})
	}
}
func TestSnapshotLargeStreamingChecksumAndExactInputBounds(t *testing.T) {
	archive := bytes.Repeat([]byte("0123456789abcdef"), (responseLimit+4096)/16)
	checksum := snapshotHash(archive)
	description, _ := parseSnapshotDescription([]byte(descriptionJSON("owned.snapshot", uint64(len(archive)), checksum)))
	var downloads atomic.Int32
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		if r.URL.Path == "/collections/owned/snapshots" {
			_, _ = io.WriteString(w, snapshotEnvelope("["+string(description.Raw)+"]"))
			return
		}
		if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Range") != "" {
			t.Error("download altered encoding or resumed")
		}
		downloads.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(archive)
	})
	sink := newSnapshotSink()
	result, err := c.DownloadSnapshot(context.Background(), unitSnapshotTarget, description, sink, SnapshotTransferOptions{MaxBytes: uint64(len(archive)), Timeout: time.Minute})
	if err != nil || !result.Complete || result.Received != uint64(len(archive)) || result.Written != result.Received || result.SHA256 != checksum || len(result.Prefix) != 1024 || len(result.Response.Body) != 0 || sink.closed.Load() != 1 {
		t.Fatal("large streaming evidence wrong", err, result.Received)
	}
	description.Size++
	sink = newSnapshotSink()
	if _, err = c.DownloadSnapshot(context.Background(), unitSnapshotTarget, description, sink, SnapshotTransferOptions{MaxBytes: description.Size}); !errors.Is(err, ErrSnapshotChanged) || downloads.Load() != 1 || sink.closed.Load() != 1 {
		t.Fatal("changed metadata downloaded", err)
	}
}
func TestSnapshotTransferFailuresRetainPartialEvidenceWithoutRedirectOrReplay(t *testing.T) {
	for _, failure := range []string{"checksum", "short", "sink", "close", "redirect", "truncated", "bound"} {
		t.Run(failure, func(t *testing.T) {
			archive := []byte("original archive bytes")
			checksum := snapshotHash(archive)
			if failure == "checksum" {
				checksum = strings.Repeat("a", 64)
			}
			d, _ := parseSnapshotDescription([]byte(descriptionJSON("owned.snapshot", uint64(len(archive)), checksum)))
			var downloads, destinations atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinations.Add(1) }))
			defer destination.Close()
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				if r.URL.Path == "/collections/owned/snapshots" {
					_, _ = io.WriteString(w, snapshotEnvelope("["+string(d.Raw)+"]"))
					return
				}
				downloads.Add(1)
				if failure == "redirect" {
					http.Redirect(w, r, destination.URL, 307)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				if failure == "truncated" {
					w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
					_, _ = w.Write(archive[:3])
					return
				}
				if failure == "bound" {
					w.(http.Flusher).Flush()
					_, _ = w.Write(append(archive, 'x'))
					return
				}
				_, _ = w.Write(archive)
			})
			sink := newSnapshotSink()
			if failure == "short" || failure == "sink" {
				sink.short = true
			}
			if failure == "close" {
				sink.closeErr = io.ErrClosedPipe
			}
			result, err := c.DownloadSnapshot(context.Background(), unitSnapshotTarget, d, sink, SnapshotTransferOptions{MaxBytes: uint64(len(archive))})
			if err == nil || result.Complete || downloads.Load() != 1 || destinations.Load() != 0 || sink.closed.Load() != 1 {
				t.Fatal("failed transfer reported complete", failure, err)
			}
			if failure != "redirect" {
				if !result.Response.Incomplete || result.Response.Status != 200 {
					t.Fatal("original incomplete HTTP evidence lost", result)
				}
				next := newSnapshotSink()
				_, err = c.DownloadSnapshot(context.Background(), unitSnapshotTarget, d, next, SnapshotTransferOptions{MaxBytes: 100})
				if !errors.Is(err, ErrRetired) || downloads.Load() != 1 {
					t.Fatal("failed transfer replayed", err)
				}
			}
		})
	}
}

type blockingSnapshotSink struct {
	started, closed chan struct{}
	count           atomic.Int32
}

func (s *blockingSnapshotSink) Write(p []byte) (int, error) {
	close(s.started)
	<-s.closed
	return 0, io.ErrClosedPipe
}
func (s *blockingSnapshotSink) Close() error { s.count.Add(1); close(s.closed); return nil }
func TestSnapshotCancelAndOriginalCloseAcknowledgeBlockedDestination(t *testing.T) {
	for _, closeClient := range []bool{false, true} {
		t.Run(fmt.Sprint(closeClient), func(t *testing.T) {
			raw := []byte("original snapshot")
			d, _ := parseSnapshotDescription([]byte(descriptionJSON("s.snapshot", uint64(len(raw)), snapshotHash(raw))))
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				if strings.HasSuffix(r.URL.Path, "/snapshots") {
					_, _ = io.WriteString(w, snapshotEnvelope("["+string(d.Raw)+"]"))
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(raw)
			})
			sink := &blockingSnapshotSink{started: make(chan struct{}), closed: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := c.DownloadSnapshot(ctx, unitSnapshotTarget, d, sink, SnapshotTransferOptions{MaxBytes: 100})
				done <- err
			}()
			<-sink.started
			if closeClient {
				c.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) {
					t.Fatal("original cancellation wrong", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original transfer did not acknowledge destination close")
			}
			if sink.count.Load() != 1 {
				t.Fatal("owned sink closed repeatedly")
			}
		})
	}
}
func TestSnapshotStreamingUploadExactFramingHashAndOneUse(t *testing.T) {
	raw := bytes.Repeat([]byte("snapshot archive "), (requestLimit/17)+1)
	checksum := snapshotHash(raw)
	var writes atomic.Int32
	c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		writes.Add(1)
		if r.URL.Query().Get("checksum") != checksum || r.URL.Query().Get("priority") != "snapshot" || r.Header.Get("Content-Length") == "" {
			t.Error("native upload options lost")
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "snapshot" || part.FileName() != "original.snapshot" {
			t.Error("native multipart filename lost", err)
			return
		}
		received, err := io.ReadAll(part)
		if err != nil || !bytes.Equal(received, raw) {
			t.Error("original upload altered", err)
		}
		if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
			t.Error("unexpected multipart fields", err)
		}
		_, _ = io.WriteString(w, snapshotEnvelope("true"))
	})
	p, err := PrepareSnapshotUpload(unitSnapshotTarget, "original.snapshot", uint64(len(raw)), checksum, "snapshot", true)
	if err != nil {
		t.Fatal(err)
	}
	name, size, hash, ok := p.UploadSource()
	if !ok || name != "original.snapshot" || size != uint64(len(raw)) || hash != checksum {
		t.Fatal("prepared upload metadata lost")
	}
	receipt, err := c.SubmitSnapshotUpload(context.Background(), p, io.NopCloser(bytes.NewReader(raw)), SnapshotTransferOptions{MaxBytes: size})
	if err != nil || receipt.State != "completed" || !receipt.HasResult || !receipt.Result || receipt.SourceBytesRead != size || receipt.SourceSHA256 != checksum {
		t.Fatal("native upload acknowledgement lost", err, receipt.State, receipt.SourceBytesRead)
	}
	if _, err = c.SubmitSnapshotUpload(context.Background(), p, io.NopCloser(bytes.NewReader(raw)), SnapshotTransferOptions{MaxBytes: size}); !errors.Is(err, ErrSnapshotUsed) || writes.Load() != 1 {
		t.Fatal("original upload replayed", err)
	}
}
func TestSnapshotUploadShortExtraChecksumAndEarlyResponseWaitForReader(t *testing.T) {
	for _, failure := range []string{"short", "extra", "checksum", "early"} {
		t.Run(failure, func(t *testing.T) {
			raw := []byte("original snapshot")
			checksum := snapshotHash(raw)
			size := uint64(len(raw))
			if failure == "short" {
				size++
			}
			if failure == "extra" {
				size--
			}
			if failure == "checksum" {
				checksum = strings.Repeat("a", 64)
			}
			var calls atomic.Int32
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				calls.Add(1)
				if failure == "early" {
					w.Header().Set("Connection", "close")
					w.WriteHeader(403)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"status":{"error":"invalid archive"}}`)
			})
			p, _ := PrepareSnapshotUpload(unitSnapshotTarget, "s.snapshot", size, checksum, "snapshot", true)
			receipt, err := c.SubmitSnapshotUpload(context.Background(), p, io.NopCloser(bytes.NewReader(raw)), SnapshotTransferOptions{MaxBytes: 100})
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || receipt.State != "uncertain" || receipt.SourceBytesRead > size {
				t.Fatal("failed native upload lost uncertainty", err, receipt)
			}
			if _, err = c.SubmitSnapshotUpload(context.Background(), p, io.NopCloser(bytes.NewReader(raw)), SnapshotTransferOptions{MaxBytes: 100}); err == nil || calls.Load() > 1 {
				t.Fatal("failed upload replayed", err)
			}
		})
	}
}

type blockingSnapshotSource struct {
	reading, closing, acknowledge, release chan struct{}
	reads, closes                          atomic.Int32
}

func (s *blockingSnapshotSource) Read([]byte) (int, error) {
	if s.reads.Add(1) == 1 {
		close(s.reading)
	}
	<-s.release
	return 0, io.ErrClosedPipe
}
func (s *blockingSnapshotSource) Close() error {
	s.closes.Add(1)
	close(s.closing)
	<-s.acknowledge
	close(s.release)
	return nil
}

func TestSnapshotUploadCancelAndOriginalCloseWaitForOwnedSourceAcknowledgement(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			source := &blockingSnapshotSource{reading: make(chan struct{}), closing: make(chan struct{}), acknowledge: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			acknowledge := func() { release.Do(func() { close(source.acknowledge) }) }
			// This cleanup runs before the client's cleanup even on a failure.
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(400)
			})
			t.Cleanup(acknowledge)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			plan, _ := PrepareSnapshotUpload(unitSnapshotTarget, "owned.snapshot", 1, "", "snapshot", true)
			done := make(chan error, 1)
			go func() {
				_, err := c.SubmitSnapshotUpload(ctx, plan, source, SnapshotTransferOptions{MaxBytes: 1})
				done <- err
			}()
			select {
			case <-source.reading:
			case <-time.After(5 * time.Second):
				t.Fatal("original source reader did not start")
			}
			if cancelFirst {
				cancel()
			}
			closed := make(chan struct{})
			go func() { c.Close(); close(closed) }()
			select {
			case <-source.closing:
			case <-time.After(5 * time.Second):
				t.Fatal("original source close did not start")
			}
			select {
			case <-done:
				t.Fatal("upload settled before original source acknowledgement")
			case <-closed:
				t.Fatal("client closed before original source acknowledgement")
			default:
			}
			acknowledge()
			select {
			case err := <-done:
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || !errors.Is(err, context.Canceled) {
					t.Fatal("original upload cancellation lost", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("upload did not settle after source acknowledgement")
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("original client did not close")
			}
			if source.closes.Load() != 1 {
				t.Fatal("original source was closed repeatedly")
			}
		})
	}
}

package qdrant

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func nativeSnapshotSubmit(t *testing.T, c *Client, p SnapshotPlan) SnapshotReceipt {
	t.Helper()
	r, err := c.SubmitSnapshot(context.Background(), p)
	if err != nil || r.State != "completed" {
		t.Fatal("native snapshot operation", err, r.State)
	}
	return r
}
func nativeSnapshotCreate(t *testing.T, c *Client, target SnapshotTarget) SnapshotDescription {
	t.Helper()
	p, err := PrepareSnapshotCreate(target, true)
	if err != nil {
		t.Fatal(err)
	}
	r := nativeSnapshotSubmit(t, c, p)
	if r.Description == nil || r.Description.Size == 0 {
		t.Fatal("native snapshot did not name an archive")
	}
	return *r.Description
}
func nativeSnapshotFile(t *testing.T, c *Client, target SnapshotTarget, d SnapshotDescription) (string, SnapshotTransfer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owned.snapshot")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.DownloadSnapshot(context.Background(), target, d, f, SnapshotTransferOptions{MaxBytes: d.Size + 1, Timeout: 2 * time.Minute})
	if err != nil || !result.Complete || result.Received != d.Size || result.Written != d.Size || len(result.Response.Body) != 0 || len(result.Prefix) > 1024 {
		t.Fatal("native snapshot transfer incomplete", err, result.Received, d.Size)
	}
	check, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, check)
	if err != nil || uint64(n) != d.Size || hex.EncodeToString(digest.Sum(nil)) != result.SHA256 {
		t.Fatal("local archive differs from native transfer", err)
	}
	return path, result
}
func nativeSnapshotUpload(t *testing.T, c *Client, target SnapshotTarget, path string, result SnapshotTransfer) {
	t.Helper()
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PrepareSnapshotUpload(target, "original.snapshot", result.Received, result.SHA256, "snapshot", true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.SubmitSnapshotUpload(context.Background(), p, source, SnapshotTransferOptions{MaxBytes: result.Received, Timeout: 2 * time.Minute})
	nativeResult := receipt.HasResult && receipt.Result && !receipt.HasNullResult
	if target.Scope == ShardSnapshots {
		nativeResult = receipt.HasNullResult && !receipt.HasResult
	}
	if err != nil || receipt.State != "completed" || !nativeResult || receipt.SourceBytesRead != result.Received || receipt.SourceSHA256 != result.SHA256 {
		t.Fatal("native uploaded recovery incomplete", target.Scope, err, receipt.State, receipt.HasResult, receipt.Result, receipt.SourceBytesRead, receipt.SourceSHA256, result.SHA256, string(receipt.Response.Body))
	}
}
func nativeSnapshotCount(t *testing.T, c *Client, name string, want uint64) {
	t.Helper()
	count, _, err := c.Count(context.Background(), name, nil)
	if err != nil || count != want {
		t.Fatal("native recovered collection count", err, count, want)
	}
	if want == 1 {
		rows, _, err := c.RetrievePoints(context.Background(), name, []json.RawMessage{[]byte(`18446744073709551615`)})
		if err != nil || len(rows) != 1 || string(rows[0].ID) != "18446744073709551615" {
			t.Fatal("native original snapshot identity lost", err)
		}
		fields, err := object(rows[0].Payload)
		var text string
		if err != nil || string(fields["n"]) != "9007199254740993" || decodeJSON(fields["text"], &text) != nil || text != "原快照🐱" {
			t.Fatal("native original snapshot payload precision lost", err)
		}
	}
}

type nativeStartedSnapshotSink struct {
	*snapshotSink
	started chan struct{}
	once    sync.Once
}

func (s *nativeStartedSnapshotSink) Write(raw []byte) (int, error) {
	n, err := s.snapshotSink.Write(raw)
	s.once.Do(func() { close(s.started) })
	return n, err
}
func TestQdrantNativeSnapshotCollectionShardStorageCRUDAndRecovery(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"},"shard_number":1}`)
	nativeUpsert(t, c, name, `{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"n":9007199254740993,"text":"原快照🐱"}}]}`)
	targets := []SnapshotTarget{{Scope: CollectionSnapshots, Collection: name}, {Scope: ShardSnapshots, Collection: name, Shard: 0}, {Scope: StorageSnapshots}}
	for _, target := range targets {
		d := nativeSnapshotCreate(t, c, target)
		listed, _, err := c.ListSnapshots(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range listed {
			if candidate.Name == d.Name && candidate.Size == d.Size {
				found = true
			}
		}
		if !found {
			t.Fatal("created native snapshot not listed")
		}
		path, result := nativeSnapshotFile(t, c, target, d)
		if target.Scope != StorageSnapshots {
			nativeUpsert(t, c, name, `{"points":[{"id":2,"vector":[0,1]}]}`)
			nativeSnapshotUpload(t, c, target, path, result)
			nativeSnapshotCount(t, c, name, 1)
			location := "file:///qdrant/snapshots/" + name + "/" + d.Name
			if target.Scope == ShardSnapshots {
				location = d.Name
			}
			p, err := PrepareSnapshotRecovery(target, SnapshotRecovery{Location: location, Priority: "snapshot", Checksum: result.SHA256}, true)
			if err != nil {
				t.Fatal(err)
			}
			nativeUpsert(t, c, name, `{"points":[{"id":2,"vector":[0,1]}]}`)
			r := nativeSnapshotSubmit(t, c, p)
			if !r.HasResult || !r.Result {
				t.Fatal("native URL/local recovery boolean absent")
			}
			nativeSnapshotCount(t, c, name, 1)
		}
		p, _ := PrepareSnapshotDelete(target, d.Name, true)
		r := nativeSnapshotSubmit(t, c, p)
		if !r.HasResult || !r.Result {
			t.Fatal("native snapshot deletion false")
		}
		if _, err := c.SubmitSnapshot(context.Background(), p); !errors.Is(err, ErrSnapshotUsed) {
			t.Fatal("native delete replayed", err)
		}
	}
	// Streaming a live shard uses no fabricated listed archive identity/size.
	f, err := os.Create(filepath.Join(t.TempDir(), "live.snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	live, err := c.StreamShardSnapshot(context.Background(), targets[1], f, SnapshotTransferOptions{MaxBytes: 64 << 20, Timeout: time.Minute})
	if err != nil || !live.Complete || live.Received == 0 || live.Name != "" || live.ExpectedChecksum != "" {
		t.Fatal("native live shard stream", err)
	}
	// Background native admission identifies neither an operation nor an archive.
	listed, _, err := c.ListSnapshots(context.Background(), targets[0])
	if err != nil {
		t.Fatal(err)
	}
	p, _ := PrepareSnapshotCreate(targets[0], false)
	r, err := c.SubmitSnapshot(context.Background(), p)
	if err != nil || r.State != "enqueued" || r.Description != nil || r.HasResult {
		t.Fatal("background snapshot fabricated completion", err, r.State)
	}
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		current, _, err := c.ListSnapshots(context.Background(), targets[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(current) > len(listed) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native background snapshot never appeared")
}
func TestQdrantNativeSnapshotLargeStreamingDownloadUploadAndURLRecovery(t *testing.T) {
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":true,"shard_number":1}`)
	// High entropy payload avoids a false large-file test after storage compression.
	for batch := 0; batch < 6; batch++ {
		rows := make([]map[string]any, 10)
		for i := range rows {
			raw := make([]byte, 400<<10)
			if _, err := rand.Read(raw); err != nil {
				t.Fatal(err)
			}
			rows[i] = map[string]any{"id": batch*10 + i + 1, "vector": []int{1, 0}, "payload": map[string]any{"blob": base64.StdEncoding.EncodeToString(raw)}}
		}
		body, _ := json.Marshal(map[string]any{"points": rows})
		nativeUpsert(t, c, name, string(body))
	}
	target := SnapshotTarget{Scope: CollectionSnapshots, Collection: name}
	d := nativeSnapshotCreate(t, c, target)
	if d.Size <= responseLimit {
		t.Fatal("owned native snapshot did not exceed JSON response buffer", d.Size)
	}
	path, result := nativeSnapshotFile(t, c, target, d)
	destination := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"},"on_disk_payload":true}`)
	nativeSnapshotUpload(t, c, SnapshotTarget{Scope: CollectionSnapshots, Collection: destination}, path, result)
	nativeSnapshotCount(t, c, destination, 60)
	cfgRaw, err := os.ReadFile(os.Getenv("NYADB_QDRANT_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Gateway string `json:"host_gateway"`
	}
	if json.Unmarshal(cfgRaw, &cfg) != nil || net.ParseIP(cfg.Gateway) == nil {
		t.Fatal("owned gateway unavailable")
	}
	var fetches atomic.Int32
	key := "owned-original-source-key"
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != key {
			w.WriteHeader(403)
			return
		}
		fetches.Add(1)
		f, err := os.Open(path)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, f)
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	location := "http://" + net.JoinHostPort(cfg.Gateway, strconv.Itoa(port)) + "/original.snapshot"
	p, err := PrepareSnapshotRecovery(SnapshotTarget{Scope: CollectionSnapshots, Collection: destination}, SnapshotRecovery{Location: location, Priority: "snapshot", Checksum: result.SHA256, APIKey: key}, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(p.Preview().Body, []byte(key)) {
		t.Fatal("source key exposed in preview")
	}
	nativeRun(t, c, Request{Method: "POST", Path: "/collections/" + destination + "/points/delete?wait=true", Body: []byte(`{"points":[1]}`)})
	receipt := nativeSnapshotSubmit(t, c, p)
	if !receipt.Result || fetches.Load() != 1 {
		t.Fatal("native authenticated URL recovery did not fetch once")
	}
	nativeSnapshotCount(t, c, destination, 60)
	t.Logf("Owned native snapshot %d bytes streamed, SHA256 verified, uploaded and recovered from authenticated URL", d.Size)
}
func TestQdrantNativeSnapshotPermissionsFailureAndOriginalMutationNoReplay(t *testing.T) {
	cfg, secret, readonly := nativeFixture(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	target := SnapshotTarget{Scope: CollectionSnapshots, Collection: name}
	d := nativeSnapshotCreate(t, c, target)
	if cfg.Auth == "api_key" {
		r, err := Open(context.Background(), cfg, readonly)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if _, _, err = r.ListSnapshots(context.Background(), target); err != nil {
			t.Fatal("native readonly snapshot list", err)
		}
		p, _ := PrepareSnapshotCreate(target, true)
		receipt, err := r.SubmitSnapshot(context.Background(), p)
		var interrupted *Interrupted
		if !errors.As(err, &interrupted) || receipt.Response.Status != 403 || receipt.State != "uncertain" {
			t.Fatal("native readonly mutation denial", err, receipt.Response.Status)
		}
	}
	p, err := PrepareSnapshotUpload(target, "invalid-native.snapshot", 7, snapshotHash([]byte("invalid")), "snapshot", true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := c.SubmitSnapshotUpload(context.Background(), p, io.NopCloser(strings.NewReader("invalid")), SnapshotTransferOptions{MaxBytes: 100})
	if err == nil || receipt.State != "uncertain" || receipt.Response.Status < 400 || len(receipt.Response.Body) == 0 {
		t.Fatal("native corrupt archive did not retain original failure", err, receipt.State)
	}
	fresh, err := Open(context.Background(), cfg, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err = fresh.SubmitSnapshotUpload(context.Background(), p, io.NopCloser(strings.NewReader("invalid")), SnapshotTransferOptions{MaxBytes: 100}); !errors.Is(err, ErrSnapshotUsed) {
		t.Fatal("failed upload replayed on another original client", err)
	}
	listed, _, err := fresh.ListSnapshots(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	// Native upload moves the complete file before trying collection recovery.
	failedFile := false
	for _, s := range listed {
		if s.Name == "invalid-native.snapshot" {
			failedFile = true
		}
	}
	if !failedFile {
		t.Fatal("native failed recovery artifact was incorrectly assumed rolled back")
	}
	sink := newSnapshotSink()
	bad := d
	bad.Checksum = strings.Repeat("0", 64)
	if _, err = fresh.DownloadSnapshot(context.Background(), target, bad, sink, SnapshotTransferOptions{MaxBytes: d.Size}); !errors.Is(err, ErrSnapshotChanged) || sink.count != 0 {
		t.Fatal("changed selected snapshot downloaded", err)
	}
}
func TestQdrantNativeSnapshotLostCanceledTransfersAndOriginalClose(t *testing.T) {
	cfg, secret, _ := nativeFixture(t)
	for _, fault := range []string{"lost-create", "canceled-create", "lost-download", "canceled-download", "close-download", "lost-upload", "canceled-upload", "close-upload"} {
		t.Run(fault, func(t *testing.T) {
			admin, err := Open(context.Background(), cfg, secret)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			name := nativeCollection(t, admin, `{"vectors":{"size":2,"distance":"Dot"}}`)
			target := SnapshotTarget{Scope: CollectionSnapshots, Collection: name}
			d := nativeSnapshotCreate(t, admin, target)
			archive := nativeRun(t, admin, Request{Method: "GET", Path: "/collections/" + name + "/snapshots/" + d.Name}).Body
			var attempts atomic.Int32
			started, ack, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if (fault == "canceled-upload" || fault == "close-upload") && strings.HasSuffix(r.URL.Path, "/upload") {
					attempts.Add(1)
					var byte [1]byte
					_, _ = r.Body.Read(byte[:])
					close(started)
					// Drain the original body so net/http can observe the peer
					// close; an unread body prevents server cancellation detection.
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-release:
					}
					close(ack)
					return
				}
				raw, _ := io.ReadAll(io.LimitReader(r.Body, requestLimit+1))
				upstream, err := Open(r.Context(), cfg, secret)
				if err != nil {
					w.WriteHeader(502)
					return
				}
				defer upstream.Close()
				var response Response
				if fault == "lost-upload" && strings.HasSuffix(r.URL.Path, "/upload") {
					// Forward the original multipart bytes once to the owned
					// native node, then discard its real recovery receipt.
					req, e := upstream.transferRequest(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type")}, bytes.NewReader(raw), int64(len(raw)))
					if e != nil {
						w.WriteHeader(502)
						return
					}
					res, e := upstream.http.Do(req)
					if e != nil {
						w.WriteHeader(502)
						return
					}
					defer res.Body.Close()
					response.Status, response.ContentType = res.StatusCode, res.Header.Get("Content-Type")
					response.Body, err = io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
				} else {
					response, err = upstream.Do(r.Context(), Request{Method: r.Method, Path: r.URL.RequestURI(), Body: raw})
				}
				if err != nil && response.Status == 0 {
					w.WriteHeader(502)
					return
				}
				mutate := strings.HasSuffix(r.URL.Path, "/snapshots") && r.Method == "POST"
				download := strings.HasSuffix(r.URL.Path, "/"+d.Name) && r.Method == "GET"
				if mutate && (fault == "lost-create" || fault == "canceled-create") || download || fault == "lost-upload" && strings.HasSuffix(r.URL.Path, "/upload") {
					attempts.Add(1)
					w.Header().Set("Content-Type", response.ContentType)
					if download {
						w.Header().Set("Content-Length", fmt.Sprint(len(response.Body)))
						w.WriteHeader(response.Status)
						_, _ = w.Write(response.Body[:min(1024, len(response.Body))])
						w.(http.Flusher).Flush()
					}
					close(started)
					if strings.HasPrefix(fault, "lost") {
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						close(ack)
						return
					}
					select {
					case <-r.Context().Done():
					case <-release:
					}
					close(ack)
					return
				}
				w.Header().Set("Content-Type", response.ContentType)
				w.WriteHeader(response.Status)
				_, _ = w.Write(response.Body)
			}))
			defer relay.Close()
			defer close(release)
			ca := filepath.Join(t.TempDir(), "relay-ca.pem")
			if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: relay.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			proxied := cfg
			proxied.Endpoint = relay.URL
			proxied.CAFile, proxied.ServerName = ca, "example.com"
			proxied.ClientCertFile, proxied.ClientKeyFile = "", ""
			c, err := Open(context.Background(), proxied, secret)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			var receipt SnapshotReceipt
			var result SnapshotTransfer
			var plan SnapshotPlan
			sink := &nativeStartedSnapshotSink{snapshotSink: newSnapshotSink(), started: make(chan struct{})}
			if strings.HasSuffix(fault, "create") {
				plan, _ = PrepareSnapshotCreate(target, true)
				go func() { var err error; receipt, err = c.SubmitSnapshot(ctx, plan); done <- err }()
			} else if strings.HasSuffix(fault, "upload") {
				if fault == "lost-upload" {
					nativeUpsert(t, admin, name, `{"points":[{"id":1,"vector":[1,0]}]}`)
				}
				plan, _ = PrepareSnapshotUpload(target, "original.snapshot", uint64(len(archive)), snapshotHash(archive), "snapshot", true)
				go func() {
					var err error
					receipt, err = c.SubmitSnapshotUpload(ctx, plan, io.NopCloser(bytes.NewReader(archive)), SnapshotTransferOptions{MaxBytes: uint64(len(archive))})
					done <- err
				}()
			} else {
				go func() {
					var err error
					result, err = c.DownloadSnapshot(ctx, target, d, sink, SnapshotTransferOptions{MaxBytes: d.Size})
					done <- err
				}()
			}
			select {
			case <-started:
			case <-time.After(15 * time.Second):
				t.Fatal("native original transfer never reached relay")
			}
			if fault == "canceled-download" || fault == "close-download" {
				select {
				case <-sink.started:
				case <-time.After(15 * time.Second):
					t.Fatal("native original archive bytes never reached destination")
				}
			}
			if strings.HasPrefix(fault, "canceled") {
				cancel()
			}
			if strings.HasPrefix(fault, "close") {
				c.Close()
			}
			select {
			case err = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("native original transfer failed to acknowledge cancellation")
			}
			var interrupted *Interrupted
			if !errors.As(err, &interrupted) || interrupted.WriteOutcomeUnknown != (strings.HasSuffix(fault, "create") || strings.HasSuffix(fault, "upload")) {
				t.Fatal("original snapshot uncertainty wrong", err)
			}
			if strings.HasSuffix(fault, "download") {
				if result.Complete || result.Received == 0 || !result.Response.Incomplete {
					t.Fatal("partial native archive evidence lost", result.Received)
				}
			} else {
				if receipt.State != "uncertain" {
					t.Fatal("native mutation outcome cleared", receipt.State)
				}
				original, err := Open(context.Background(), cfg, secret)
				if err != nil {
					t.Fatal(err)
				}
				defer original.Close()
				if strings.HasSuffix(fault, "upload") {
					_, err = original.SubmitSnapshotUpload(context.Background(), plan, io.NopCloser(bytes.NewReader(archive)), SnapshotTransferOptions{MaxBytes: uint64(len(archive))})
				} else {
					_, err = original.SubmitSnapshot(context.Background(), plan)
				}
				if !errors.Is(err, ErrSnapshotUsed) {
					t.Fatal("lost original mutation replayed", err)
				}
			}
			if attempts.Load() != 1 {
				t.Fatal("original transfer submitted multiple times")
			}
			if fault == "lost-upload" {
				count, _, err := admin.Count(context.Background(), name, nil)
				if err != nil || count != 0 {
					t.Fatal("lost upload did not retain its actual native recovery", err, count)
				}
			}
			select {
			case <-ack:
			case <-time.After(10 * time.Second):
				t.Fatal("original relay did not settle")
			}
			if strings.HasSuffix(fault, "create") {
				current, _, err := admin.ListSnapshots(context.Background(), target)
				if err != nil || len(current) != 2 {
					t.Fatal("original native creation effect lost or replayed", err, len(current))
				}
			}
		})
	}
}

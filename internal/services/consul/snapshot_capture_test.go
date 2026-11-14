package consul

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

type snapshotWriterFunc func([]byte) (int, error)

func (f snapshotWriterFunc) Write(p []byte) (int, error) { return f(p) }

func snapshotTestOriginal(t *testing.T) []byte {
	return snapshotTestGzip(t, snapshotTestTar(t, snapshotTestMembers([]byte("abc"), []byte(`{"Version":1,"ID":"original","Index":1,"Term":2,"Size":3}`), false)))
}

func TestSnapshotCaptureOriginalPlanScopePrivateReceiptAndOutput(t *testing.T) {
	original := snapshotTestOriginal(t)
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.RequestURI() != "/v1/snapshot?dc=dc1&stale=" || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("X-Consul-Token") != "private-token" {
			t.Error("original snapshot scope or method changed")
		}
		w.Header().Set("Trailer", "X-Original-Trailer")
		_, _ = w.Write(original)
		w.Header().Set("X-Original-Trailer", "complete")
	})
	c.config.Datacenter, c.config.Consistency, c.config.Prefix, c.config.Auth, c.token = "dc1", "stale", "must-not-filter/", "token", "private-token"
	p, err := c.prepareSnapshotArchive(snapshotTestLimits)
	if err != nil || calls.Load() != 0 {
		t.Fatal("preparation dispatched")
	}
	var output bytes.Buffer
	r, err := c.captureSnapshotArchive(context.Background(), p, &output)
	defer r.clear()
	if err != nil || !r.attempted || !r.ended || !r.eof || !r.httpComplete || !r.sourceComplete || !r.outputAcknowledged || !r.completed || !bytes.Equal(r.body, original) || !bytes.Equal(output.Bytes(), original) || r.trailer.Get("X-Original-Trailer") != "complete" {
		t.Fatal("original capture incomplete", err)
	}
	if c.LastResponse().Kind != "" || c.LastResponse().Body != nil {
		t.Fatal("private archive entered generic response cache")
	}
	if _, err := c.captureSnapshotArchive(context.Background(), p, io.Discard); !errors.Is(err, ErrPlanUsed) || calls.Load() != 1 {
		t.Fatal("snapshot replayed")
	}
	r.clear()
	if r.body != nil || r.proof.metadata != nil || r.completed {
		t.Fatal("private capture not released")
	}
}

func TestSnapshotCaptureHTTPArchiveAndOutputProofRemainIndependent(t *testing.T) {
	original := snapshotTestOriginal(t)
	for _, name := range []string{"denied", "redirect", "short-http", "bad-gzip", "capture-bound", "short-output", "failed-output", "mutated-output"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch name {
				case "denied":
					w.WriteHeader(403)
					_, _ = w.Write([]byte("Permission denied"))
					return
				case "redirect":
					w.Header().Set("Location", "/foreign")
					w.WriteHeader(302)
					return
				case "short-http":
					w.Header().Set("Content-Length", "999999")
				}
				body := bytes.Clone(original)
				if name == "bad-gzip" {
					body[len(body)-8] ^= 1
				}
				_, _ = w.Write(body)
			})
			limits := snapshotTestLimits
			if name == "capture-bound" {
				limits.compressed = int64(len(original) - 1)
			}
			p, _ := c.prepareSnapshotArchive(limits)
			var writes int
			output := snapshotWriterFunc(func(body []byte) (int, error) {
				writes++
				if name == "short-output" {
					return len(body) - 1, nil
				}
				if name == "failed-output" {
					return len(body), errors.New("private sink error")
				}
				if name == "mutated-output" {
					clear(body)
				}
				return len(body), nil
			})
			r, err := c.captureSnapshotArchive(context.Background(), p, output)
			defer r.clear()
			if !r.ended || !r.attempted || calls.Load() != 1 || (err == nil) != (name == "mutated-output") {
				t.Fatal("original failure or completion changed", err)
			}
			if name == "short-output" || name == "failed-output" || name == "mutated-output" {
				if !r.sourceComplete || !r.proof.metadataValid || writes != 1 || r.outputAcknowledged != (name == "mutated-output") || !bytes.Equal(r.body, original) {
					t.Fatal("output replaced original source proof")
				}
			} else if writes != 0 || r.sourceComplete || r.completed {
				t.Fatal("invalid original reached output")
			}
			if name == "bad-gzip" && (!r.httpComplete || !r.eof || r.proof.gzipComplete) {
				t.Fatal("HTTP completion substituted for gzip integrity")
			}
			if (name == "short-http" || name == "capture-bound") && (r.eof || r.httpComplete || c.Alive()) {
				t.Fatal("incomplete original HTTP remained available")
			}
			if _, unknown := c.UnknownResponse(); unknown {
				t.Fatal("save-only read created an unknown write")
			}
		})
	}
}

func TestSnapshotCaptureCancellationAndCloseWaitForOriginalOutput(t *testing.T) {
	original := snapshotTestOriginal(t)
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(original) })
	p, _ := c.prepareSnapshotArchive(snapshotTestLimits)
	entered, release, finished, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var result snapshotArchiveCapture
	var captureErr error
	go func() {
		result, captureErr = c.captureSnapshotArchive(ctx, p, snapshotWriterFunc(func(body []byte) (int, error) {
			close(entered)
			<-release
			return len(body), nil
		}))
		close(finished)
	}()
	wait(t, entered)
	cancel()
	other, _ := c.prepareSnapshotArchive(snapshotTestLimits)
	if _, err := c.captureSnapshotArchive(context.Background(), other, io.Discard); !errors.Is(err, ErrBusy) {
		t.Fatal("active original output did not reserve connection")
	}
	go func() { _ = c.Close(); close(closed) }()
	wait(t, c.ctx.Done())
	select {
	case <-closed:
		t.Fatal("close detached original writer")
	default:
	}
	select {
	case <-finished:
		t.Fatal("cancel detached original writer")
	default:
	}
	close(release)
	wait(t, finished)
	wait(t, closed)
	defer result.clear()
	if !errors.Is(captureErr, context.Canceled) || !result.ended || !result.canceled || !result.sourceComplete || !result.outputAcknowledged || result.completed {
		t.Fatal("original output acknowledgement lost after cancellation", captureErr)
	}
}

func TestSnapshotCapturePreDispatchCancellationAndReadPreserveUnknownWrite(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write(snapshotTestOriginal(t)) })
	c.unknown = &Response{WriteOutcomeUnknown: true, Body: []byte("original-write")}
	p, _ := c.prepareSnapshotArchive(snapshotTestLimits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := c.captureSnapshotArchive(ctx, p, io.Discard)
	if !errors.Is(err, context.Canceled) || r.attempted || !r.ended || !r.canceled || calls.Load() != 0 {
		t.Fatal("cancelled plan dispatched")
	}
	p, _ = c.prepareSnapshotArchive(snapshotTestLimits)
	r, err = c.captureSnapshotArchive(context.Background(), p, io.Discard)
	defer r.clear()
	unknown, ok := c.UnknownResponse()
	if err != nil || !r.completed || !ok || !bytes.Equal(unknown.Body, []byte("original-write")) || calls.Load() != 1 {
		t.Fatal("snapshot read erased prior unknown write")
	}
}

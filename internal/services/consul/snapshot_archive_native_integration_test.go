package consul

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestConsulNativeSnapshotArchiveOriginalScopeChecksumsAndPrivateOutput(t *testing.T) {
	c, cfg, token := nativeClient(t)
	inside, outside := []byte("snapshot-inside-original\x00\xff"), []byte("snapshot-outside-original\xff\x00")
	nativeDo(t, c, putRequest(keyFor(t, "inside/value"), inside))
	nativeDo(t, c, putRequest(keyFor(t, "outside/value"), outside))
	prefix := keyFor(t, "inside/")
	for _, consistency := range []string{"default", "stale"} {
		t.Run(consistency, func(t *testing.T) {
			cfg.Prefix, cfg.Datacenter, cfg.Consistency = prefix, "dc1", consistency
			original, err := New(cfg, token)
			if err != nil {
				t.Fatal("original snapshot client unavailable", err)
			}
			defer original.Close()
			plan, err := original.prepareSnapshotArchive(snapshotTestLimits)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			receipt, err := original.captureSnapshotArchive(context.Background(), plan, &output)
			defer receipt.clear()
			defer clear(output.Bytes())
			if err != nil || !receipt.completed || !receipt.proof.metadataValid || receipt.proof.version != 1 || receipt.status != 200 || !bytes.Equal(output.Bytes(), receipt.body) {
				t.Fatal("original native archive capture failed", err)
			}
			z, err := gzip.NewReader(bytes.NewReader(receipt.body))
			if err != nil {
				t.Fatal("native gzip unavailable")
			}
			expanded, err := io.ReadAll(io.LimitReader(z, snapshotTestLimits.expanded+1))
			_ = z.Close()
			defer clear(expanded)
			if err != nil || int64(len(expanded)) > snapshotTestLimits.expanded || !bytes.Contains(expanded, inside) || !bytes.Contains(expanded, outside) || token != "" && !bytes.Contains(expanded, []byte(token)) {
				t.Fatal("native full scope or original private state missing")
			}
			if original.LastResponse().Body != nil {
				t.Fatal("native archive entered generic response cache")
			}
			if path := os.Getenv("NYADB_CONSUL_SNAPSHOT_PRIVATE_OUTPUT"); path != "" && consistency == "default" {
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal("owned private native inspector output unavailable")
				}
				n, writeErr := file.Write(output.Bytes())
				syncErr := file.Sync()
				closeErr := file.Close()
				if n != output.Len() || writeErr != nil || syncErr != nil || closeErr != nil {
					t.Fatal("owned private native inspector output incomplete")
				}
			}
		})
	}
}

func TestConsulNativeSnapshotArchiveUpstreamCompletionAndOriginalTransferFaults(t *testing.T) {
	_, cfg, token := nativeClient(t)
	upstream, err := New(cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	for _, mode := range []string{"complete", "truncated-http", "corrupt-gzip", "http500", "short-output", "failed-output", "cancel-body"} {
		t.Run(mode, func(t *testing.T) {
			var upstreamComplete atomic.Bool
			var upstreamCalls atomic.Int32
			ready := make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
				upstreamCalls.Add(1)
				request, _ := http.NewRequestWithContext(incoming.Context(), "GET", cfg.Endpoint+"/v1/snapshot", nil)
				if token != "" {
					request.Header.Set("X-Consul-Token", token)
				}
				response, err := upstream.http.Do(request)
				if err != nil {
					t.Error("owned upstream snapshot unavailable")
					http.Error(w, "upstream unavailable", 502)
					return
				}
				body, err := io.ReadAll(io.LimitReader(response.Body, snapshotTestLimits.compressed+1))
				closeErr := response.Body.Close()
				defer clear(body)
				if err != nil || closeErr != nil || int64(len(body)) > snapshotTestLimits.compressed || response.StatusCode != 200 {
					t.Error("owned upstream snapshot incomplete")
					http.Error(w, "upstream incomplete", 502)
					return
				}
				proof, err := inspectSnapshotArchive(incoming.Context(), body, snapshotTestLimits)
				if err != nil || !proof.metadataValid {
					t.Error("owned upstream native proof invalid")
					http.Error(w, "upstream invalid", 502)
					return
				}
				clear(proof.metadata)
				clear(proof.checksums)
				upstreamComplete.Store(true)
				switch mode {
				case "truncated-http":
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
					_, _ = w.Write(body[:len(body)/2])
					return
				case "corrupt-gzip":
					body[len(body)-8] ^= 1
				case "http500":
					w.WriteHeader(500)
				case "cancel-body":
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
					_, _ = w.Write(body[:len(body)/2])
					w.(http.Flusher).Flush()
					close(ready)
					<-incoming.Context().Done()
					return
				}
				_, _ = w.Write(body)
			}))
			defer proxy.Close()
			original, err := New(Config{Endpoint: proxy.URL}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			plan, _ := original.prepareSnapshotArchive(snapshotTestLimits)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel-body" {
				go func() {
					select {
					case <-ready:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			var writes int
			receipt, err := original.captureSnapshotArchive(ctx, plan, snapshotWriterFunc(func(body []byte) (int, error) {
				writes++
				if mode == "short-output" {
					return len(body) - 1, nil
				}
				if mode == "failed-output" {
					return len(body), errors.New("owned sink fault")
				}
				return len(body), nil
			}))
			defer receipt.clear()
			if !upstreamComplete.Load() || upstreamCalls.Load() != 1 || !receipt.attempted || !receipt.ended || receipt.completed != (mode == "complete") || (err == nil) != (mode == "complete") {
				t.Fatal("original native upstream or fault evidence changed", err)
			}
			if mode == "short-output" || mode == "failed-output" {
				if !receipt.sourceComplete || receipt.outputAcknowledged || writes != 1 {
					t.Fatal("source proof replaced output acknowledgement")
				}
			} else if mode != "complete" && (writes != 0 || receipt.sourceComplete) {
				t.Fatal("failed source reached original output")
			}
			if mode == "cancel-body" && (!receipt.canceled || !errors.Is(err, context.Canceled)) {
				t.Fatal("original cancellation not acknowledged")
			}
			if mode == "corrupt-gzip" && (!receipt.httpComplete || receipt.proof.gzipComplete) {
				t.Fatal("HTTP EOF substituted for original gzip integrity")
			}
			if _, err := original.captureSnapshotArchive(context.Background(), plan, io.Discard); err == nil || upstreamCalls.Load() != 1 {
				t.Fatal("native snapshot replayed")
			}
		})
	}
}

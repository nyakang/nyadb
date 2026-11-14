package consul

import (
	"context"
	"crypto/rand"
	"github.com/nyakang/nyadb/internal/db/contract"
	"io"
	"testing"
)

func TestHTTPArchiveRestoreNativeExactArchiveReplacesLaterKeys(t *testing.T) {
	cfg, token := nativeConfig(t)
	cfg.RestoreWrite = true
	s, err := OpenNativeSession(context.Background(), 919, cfg, token)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	l, _ := s.ReserveKV(99)
	defer l.Close()
	capture := l.(contract.KVHTTPArchiveCapturing)
	p, err := capture.PrepareHTTPArchive(contract.KVHTTPArchiveRequest{CompressedBytes: 8 << 20, ExpandedBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	source, proof, err := capture.CaptureHTTPArchive(context.Background(), p)
	if err != nil || !proof.SourceComplete {
		t.Fatal(err)
	}
	defer source.Close()
	reader, _ := source.OpenOriginal()
	archive, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	seed, _, _ := nativeClient(t)
	defer seed.Close()
	key := "nyadb-after-archive-" + rand.Text()
	if _, err = seed.Do(context.Background(), putRequest(key, []byte("later"))); err != nil {
		t.Fatal(err)
	}
	restore := l.(contract.KVHTTPArchiveRestoring)
	prepared, err := restore.PrepareHTTPArchiveRestore(contract.KVHTTPArchiveRestoreRequest{Archive: archive, ExpandedBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	result, err := restore.ExecuteHTTPArchiveRestore(context.Background(), prepared)
	if err != nil || !result.Completed || result.WriteOutcomeUnknown {
		t.Fatal("native restore acknowledgement lost", err)
	}
	verify, _, _ := nativeClient(t)
	defer verify.Close()
	path, _ := KeyPath(key)
	r, err := verify.Do(context.Background(), Request{Method: "GET", Path: path})
	if err != nil || r.Status != 404 {
		t.Fatal("archive did not replace later key", err)
	}
}

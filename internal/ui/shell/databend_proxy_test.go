package shell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDatabendWorkbenchProxyRecordsClientCanceledLogin(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		close(entered)
		<-req.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/session/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("original", "never-journal-this-secret")
	f := databendWorkbenchFixture{Endpoint: server.URL, Directory: t.TempDir()}
	go func() {
		select {
		case <-entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	status, _, body := databendWorkbenchForward(t, f, req)
	if status != http.StatusBadGateway || len(body) != 0 {
		t.Fatal("canceled login invented a successful original response")
	}
	raw, err := os.ReadFile(filepath.Join(f.Directory, "workbench-canceled-logins.jsonl"))
	if err != nil || strings.Contains(string(raw), "never-journal-this-secret") {
		t.Fatal("canceled login lost safe transport evidence", err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(raw, &recorded); err != nil || len(recorded) != 6 || recorded["test"] != t.Name() ||
		recorded["method"] != http.MethodPost || recorded["path"] != "/v1/session/login" || recorded["phase"] != "request" ||
		recorded["client_canceled"] != true || recorded["upstream_canceled"] != true {
		t.Fatal("cancellation evidence became an executor acknowledgement", err, recorded)
	}
}

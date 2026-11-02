package sqlworkspace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/sqltext"
	"github.com/nyakang/nyadb/internal/services/aisql"
	"github.com/nyakang/nyadb/internal/ui/theme"
)

type aiTestRepo struct {
	mu  sync.Mutex
	raw string
}

func (r *aiTestRepo) Load(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.raw, nil
}
func (r *aiTestRepo) CompareAndSwap(_ context.Context, before, after string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.raw != before {
		return aisql.ErrConflict
	}
	r.raw = after
	return nil
}
func aiFixture(t *testing.T, handler http.HandlerFunc) (*State, *Document, *ui.Tester) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store := aisql.NewStore(&aiTestRepo{}, nil)
	before, _ := store.Load(context.Background())
	c := aisql.Defaults()
	c.Endpoint, c.Model, c.Anonymous, c.Stream = server.URL+"/v1", "owned-model", true, false
	if _, e := store.Save(context.Background(), before, c, "", false); e != nil {
		t.Fatal(e)
	}
	s := New(Dependencies{CompletionCatalog: func(id int64, namespace string) sqltext.CompletionCatalog {
		if id != 7 || namespace != "original" {
			t.Fatal("AI read another scope")
		}
		return sqltext.CompletionCatalog{Dialect: "sqlite", Objects: []sqltext.CompletionObject{{Name: "fixture", Columns: []string{"id", "label"}}}}
	}})
	t.Cleanup(s.CloseAll)
	s.SetAI(store, aisql.Client{})
	d := s.Open(2, "SELECT private_value;", 7, "original", false)
	tt := ui.NewTester(func(c *ui.Context) { theme.Apply(c); s.View(c, d); s.Modals(c) }, 1100, 800)
	tt.Frame()
	s.OpenAI(d)
	aiWait(t, tt, func() bool { return s.ai.loaded && s.ai.pending == nil })
	return s, d, tt
}
func aiWait(t *testing.T, tt *ui.Tester, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("AI worker did not settle")
}
func TestAISQLPreviewOptInInsertUndoAndStaleDocumentProtection(t *testing.T) {
	got := make(chan string, 2)
	s, d, tt := aiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []struct{ Content string } }
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- body.Messages[0].Content
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "```sql\nSELECT 2;\n```"}, "finish_reason": "stop"}}})
	})
	s.ai.instruction = "Return two"
	prompt, e := s.aiPrompt()
	if e != nil || strings.Contains(prompt, "private_value") || strings.Contains(prompt, "fixture") {
		t.Fatal("unselected SQL/schema leaked")
	}
	s.ai.includeSQL, s.ai.includeSchema = true, true
	prompt, e = s.aiPrompt()
	if e != nil || !strings.Contains(prompt, "private_value") || !strings.Contains(prompt, "fixture") {
		t.Fatal("explicit context missing")
	}
	if e := tt.Click("发送给 AI"); e != nil {
		t.Fatal(e)
	}
	aiWait(t, tt, func() bool { return s.ai.pending == nil })
	if !s.ai.complete || <-got != prompt || d.Editor.Buffer.String() != "SELECT private_value;" || d.result != nil {
		t.Fatal("response executed or altered original text")
	}
	if e := tt.Click("插入原 SQL 文档"); e != nil {
		t.Fatal(e)
	}
	if d.Editor.Buffer.String() != "SELECT 2;SELECT private_value;" || !d.Dirty() {
		t.Fatalf("suggestion insertion: %q", d.Editor.Buffer.String())
	}
	d.Editor.Run(editor.Undo)
	if d.Editor.Buffer.String() != "SELECT private_value;" {
		t.Fatal("AI insertion was not undoable")
	}
	s.OpenAI(d)
	aiWait(t, tt, func() bool { return s.ai.loaded && s.ai.pending == nil })
	s.ai.instruction = "Return two"
	s.requestAI()
	aiWait(t, tt, func() bool { return s.ai.pending == nil })
	d.Editor.Insert("changed")
	before := d.Editor.Buffer.String()
	s.applyAI()
	if d.Editor.Buffer.String() != before {
		t.Fatal("old proposal overwrote changed editor")
	}
}
func TestAISQLCloseAndCancelDiscardOldReceiptsAndKeepText(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	s, d, tt := aiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(stopped)
	})
	s.ai.instruction = "Return two"
	s.requestAI()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	s.closeAI()
	s.OpenAI(d)
	aiWait(t, tt, func() bool { return s.ai.loaded && s.ai.pending == nil })
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("closed request retained resources")
	}
	tt.Frame()
	if s.ai.complete || s.ai.err != "" || d.Editor.Buffer.String() != "SELECT private_value;" {
		t.Fatal("old receipt modified the new assistant scope")
	}
	s.Close(d)
	if s.ai.open || s.ai.target.doc != nil {
		t.Fatal("document close retained assistant")
	}
}

func TestAISQLModalBlocksUnderlyingExecutionKeysAndKeepsFooterVisible(t *testing.T) {
	s, d, tt := aiFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	executions := 0
	s.deps.Connected = func(int64) bool { return true }
	s.Executor = func(context.Context, int64, string, string) (contract.SQLResult, error) {
		executions++
		return contract.SQLResult{}, nil
	}
	if e := tt.Click("AI 自然语言要求"); e != nil {
		t.Fatal(e)
	}
	tt.Key(commandModifier(), ui.KeyEnter)
	tt.Key(0, ui.KeyF5)
	if executions != 0 || d.Loading() {
		t.Fatal("assistant keystrokes executed underlying SQL")
	}
	for _, label := range []string{"AI 自然语言要求", "发送给 AI", "插入原 SQL 文档", "作为新 SQL 草稿"} {
		if rect, ok := tt.Find(label); !ok || rect.Y < 0 || rect.Y+rect.H > 800 {
			t.Fatal("AI input/footer unavailable:", label)
		} else {
			t.Log(label, rect)
		}
	}
}

func TestAISettingsFormPersistsChangesWithoutChangingSQL(t *testing.T) {
	s, d, tt := aiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("saving AI settings sent a generation request")
	})
	if e := tt.Click("AI 服务设置"); e != nil {
		t.Fatal(e)
	}
	// The visible caption shares the accessible input label. Reach the
	// endpoint and then the model from the focused settings button by Tab.
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyTab)
	tt.Command("selectAll")
	tt.Type("changed-model")
	if e := tt.Click("保存 AI 设置"); e != nil {
		t.Fatal(e)
	}
	aiWait(t, tt, func() bool { return s.ai.pending == nil })
	if s.ai.err != "" || s.ai.snapshot.Config.Model != "changed-model" {
		t.Fatal("settings form failed to publish its model", s.ai.snapshot.Config.Model, s.ai.err)
	}
	s.closeAI()
	s.OpenAI(d)
	aiWait(t, tt, func() bool { return s.ai.loaded && s.ai.pending == nil })
	if s.ai.snapshot.Config.Model != "changed-model" || d.Editor.Buffer.String() != "SELECT private_value;" {
		t.Fatal("reopened settings lost configuration or modified SQL")
	}
}

func TestAICancelButtonWaitsForRequestAcknowledgement(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	s, d, tt := aiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(stopped)
	})
	s.ai.instruction = "Return two"
	if e := tt.Click("发送给 AI"); e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request never started")
	}
	if e := tt.Click("取消 AI 请求"); e != nil {
		t.Fatal(e)
	}
	aiWait(t, tt, func() bool { return s.ai.pending == nil })
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not release its HTTP resources")
	}
	if s.ai.err == "" || s.ai.complete || d.Editor.Buffer.String() != "SELECT private_value;" {
		t.Fatal("cancelled response became applicable or changed SQL")
	}
}

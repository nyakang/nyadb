package aisql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAIHTTPProtocolPlainStreamingAndSafeErrors(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-api-key" {
					t.Error("wrong endpoint or authentication")
				}
				var b map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&b)
				if string(b["stream"]) != fmt.Sprint(streaming) || string(b["store"]) != "false" || len(b["max_completion_tokens"]) == 0 {
					t.Error("protocol parameters lost")
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, ": ping\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"SELECT \"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"1;\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"SELECT 1;","tool_calls":null},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			s, snapshot, _, _ := configured(t, server.URL+"/v1", streaming)
			progress := ""
			r, e := (Client{}).Complete(context.Background(), s, Request{Config: snapshot.Config, Prompt: "Generate SQL"}, func(v string) { progress = v })
			if e != nil || r.Text != "SELECT 1;" || requests.Load() != 1 || streaming && progress != r.Text {
				t.Fatalf("protocol failed: %v", e)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "fixture-api-key / private prompt")
	}))
	defer server.Close()
	s, snapshot, _, _ := configured(t, server.URL, false)
	_, e := (Client{}).Complete(context.Background(), s, Request{Config: snapshot.Config, Prompt: "private prompt"}, nil)
	var status HTTPError
	if !errors.As(e, &status) || status.Status != 401 || strings.Contains(e.Error(), "fixture-api-key") || strings.Contains(e.Error(), "private prompt") {
		t.Fatal("service error leaked private contents")
	}
}
func TestAIRejectsIncompleteToolCallsOversizeAndRedirectWithoutRetry(t *testing.T) {
	for _, body := range []string{`{"choices":[{"message":{"content":"SELECT"},"finish_reason":"length"}]}`, `{"choices":[{"message":{"content":"SELECT","tool_calls":[{}]},"finish_reason":"stop"}]}`, `{"choices":[{"message":{"content":"SELECT","function_call":{"name":"run_sql","arguments":"{}"}},"finish_reason":"stop"}]}`, `{"choices":[{"message":{"content":"SELECT","refusal":"refused"},"finish_reason":"stop"}]}`, strings.Repeat("x", MaxText+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		s, snapshot, _, _ := configured(t, server.URL, false)
		if _, e := (Client{}).Complete(context.Background(), s, Request{Config: snapshot.Config, Prompt: "SQL"}, nil); e == nil {
			t.Fatal("incomplete/invalid suggestion accepted")
		}
		server.Close()
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	s, snapshot, _, _ := configured(t, server.URL, false)
	if _, e := (Client{}).Complete(context.Background(), s, Request{Config: snapshot.Config, Prompt: "SQL"}, nil); e == nil || redirected.Load() != 0 {
		t.Fatal("credentials followed redirect")
	}
}

func TestAIStreamRequiresCompletedTextWithoutRefusalOrCalls(t *testing.T) {
	content := "data: {\"choices\":[{\"delta\":{\"content\":\"SELECT 1;\"},\"finish_reason\":null}]}\n\n"
	stop := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	done := "data: [DONE]\n\n"
	for name, body := range map[string]string{
		"missing_terminal":     content + stop,
		"missing_stop":         content + done,
		"empty":                stop + done,
		"late_content":         content + stop + content + done,
		"duplicate_stop":       content + stop + stop + done,
		"refusal":              content + "data: {\"choices\":[{\"delta\":{\"refusal\":\"refused\"}}]}\n\n" + stop + done,
		"tool_call":            content + "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{}]}}]}\n\n" + stop + done,
		"legacy_function_call": content + "data: {\"choices\":[{\"delta\":{\"function_call\":{\"name\":\"run_sql\"}}}]}\n\n" + stop + done,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := stream(context.Background(), strings.NewReader(body), nil)
			if err == nil || result.Text != "" {
				t.Fatal("incomplete or unsupported stream became an applicable suggestion")
			}
		})
	}
	result, err := stream(context.Background(), strings.NewReader(": heartbeat\n\n"+content+stop+"data: {\"choices\":[],\"usage\":{\"total_tokens\":10}}\n\n"+done), nil)
	if err != nil || result.Text != "SELECT 1;" {
		t.Fatalf("completed stream with final usage: %v", err)
	}
}
func TestAIStreamingCancellationRevokesPartialOutput(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"SELECT\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	s, snapshot, _, _ := configured(t, server.URL, true)
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() {
		_, e := (Client{}).Complete(ctx, s, Request{Config: snapshot.Config, Prompt: "SQL"}, func(string) { cancel() })
		out <- e
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	select {
	case e := <-out:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not settle")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("request resources not cancelled")
	}
}

package aisql

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxText = 1 << 20

type Request struct {
	Config Config
	Prompt string
}
type Result struct{ Text string }
type HTTPError struct{ Status int }

func (e HTTPError) Error() string { return fmt.Sprintf("AI service returned HTTP %d", e.Status) }

type Client struct{ Transport http.RoundTripper }
type response struct {
	Choices []struct {
		Message struct {
			Content      string
			Refusal      string
			ToolCalls    json.RawMessage `json:"tool_calls"`
			FunctionCall json.RawMessage `json:"function_call"`
		} `json:"message"`
		Delta struct {
			Content      string
			Refusal      string
			ToolCalls    json.RawMessage `json:"tool_calls"`
			FunctionCall json.RawMessage `json:"function_call"`
		} `json:"delta"`
		Finish *string `json:"finish_reason"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

func (c Client) Complete(ctx context.Context, store *Store, request Request, progress func(string)) (Result, error) {
	if store == nil {
		return Result{}, ErrKey
	}
	config, e := Normalize(request.Config)
	if e != nil {
		return Result{}, e
	}
	if len(request.Prompt) == 0 || len(request.Prompt) > 256<<10 || !utf8.ValidString(request.Prompt) {
		return Result{}, ErrConfig
	}
	key, e := store.Key(config)
	if e != nil {
		return Result{}, e
	}
	body := map[string]any{"model": config.Model, "messages": []map[string]string{{"role": "user", "content": request.Prompt}}, "stream": config.Stream, "store": false, config.TokenParameter: config.MaxTokens}
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(b))
	if e != nil {
		return Result{}, ErrConfig
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	h := http.Client{Transport: c.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, e := h.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, ErrTransport
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return Result{}, HTTPError{r.StatusCode}
	}
	if config.Stream {
		return stream(ctx, r.Body, progress)
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, MaxText+1))
	if e != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, ErrResponse
	}
	if len(data) > MaxText {
		return Result{}, ErrResponse
	}
	var v response
	if !utf8.Valid(data) || json.Unmarshal(data, &v) != nil || len(v.Choices) != 1 || len(v.Error) > 0 {
		return Result{}, ErrResponse
	}
	x := v.Choices[0]
	if x.Finish == nil || *x.Finish != "stop" || x.Message.Refusal != "" || hasCalls(x.Message.ToolCalls) || hasCalls(x.Message.FunctionCall) {
		return Result{}, ErrIncomplete
	}
	if strings.TrimSpace(x.Message.Content) == "" || !utf8.ValidString(x.Message.Content) {
		return Result{}, ErrResponse
	}
	return Result{Text: x.Message.Content}, nil
}
func stream(ctx context.Context, body io.Reader, progress func(string)) (Result, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), MaxText)
	var out strings.Builder
	event := []string{}
	bytesRead := 0
	stopped := false
	done := false
	consume := func() error {
		if len(event) == 0 {
			return nil
		}
		data := strings.Join(event, "\n")
		event = nil
		if data == "[DONE]" {
			if !stopped {
				return ErrIncomplete
			}
			done = true
			return nil
		}
		if done {
			return ErrResponse
		}
		var v response
		if !utf8.ValidString(data) || json.Unmarshal([]byte(data), &v) != nil || len(v.Error) > 0 || len(v.Choices) > 1 {
			return ErrResponse
		}
		if len(v.Choices) == 0 {
			return nil
		}
		x := v.Choices[0]
		if x.Delta.Refusal != "" || hasCalls(x.Delta.ToolCalls) || hasCalls(x.Delta.FunctionCall) || stopped && (x.Delta.Content != "" || x.Finish != nil) {
			return ErrIncomplete
		}
		if x.Finish != nil {
			if *x.Finish != "stop" {
				return ErrIncomplete
			}
			stopped = true
		}
		if out.Len()+len(x.Delta.Content) > MaxText || !utf8.ValidString(x.Delta.Content) {
			return ErrResponse
		}
		out.WriteString(x.Delta.Content)
		if progress != nil {
			progress(out.String())
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		bytesRead += len(line)
		if bytesRead > 4*MaxText {
			return Result{}, ErrResponse
		}
		if line == "" {
			if e := consume(); e != nil {
				return Result{}, e
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			event = append(event, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if scanner.Err() != nil {
		return Result{}, ErrResponse
	}
	if e := consume(); e != nil {
		return Result{}, e
	}
	if !done || !stopped || strings.TrimSpace(out.String()) == "" {
		return Result{}, ErrIncomplete
	}
	return Result{Text: out.String()}, nil
}

func hasCalls(raw json.RawMessage) bool {
	v := strings.TrimSpace(string(raw))
	return v != "" && v != "null" && v != "[]"
}

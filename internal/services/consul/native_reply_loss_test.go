package consul

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestConsulNativeSessionAndTransactionReplyLossNeverReplay(t *testing.T) {
	base, cfg, token := nativeClient(t)
	for _, kind := range []string{"delete", "txn", "session-create", "session-renew", "session-destroy"} {
		for _, mode := range []string{"lost", "partial", "http500", "http408", "cancel", "close"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				key := keyFor(t, "original")
				value := []byte("actual original\x00" + mode)
				var request Request
				id := ""
				switch kind {
				case "delete":
					nativeDo(t, base, putRequest(key, value))
					request, _ = KeyDeleteRequest(key, nil, false)
				case "txn":
					request, _ = TxnRequest([]TxnOperation{{KV: &TxnKV{Verb: "check-not-exists", Key: key}}, {KV: &TxnKV{Verb: "set", Key: key, Value: &value}}, {KV: &TxnKV{Verb: "get", Key: key}}})
				case "session-create":
					raw, _ := json.Marshal(map[string]any{"Name": t.Name(), "TTL": "10m", "NodeChecks": []string{}})
					request = Request{Method: "PUT", Path: "/v1/session/create", Body: raw}
				default:
					id = createSession(t, base, "release")
					if kind == "session-renew" {
						request = Request{Method: "PUT", Path: "/v1/session/renew/" + id}
					} else {
						request = Request{Method: "PUT", Path: "/v1/session/destroy/" + id}
					}
				}
				relay, calls, committed := nativeFault(t, cfg, token, mode)
				c, err := New(relay, token)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				plan, err := c.Prepare(request)
				if err != nil || !plan.Writes() {
					t.Fatal("original write not prepared", err)
				}
				earlier, _ := c.Prepare(putRequest(key+"-later", nil))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				var result Response
				go func() { result, err = c.Execute(ctx, plan); close(done) }()
				wait(t, committed)
				if mode == "cancel" {
					cancel()
				} else if mode == "close" {
					c.Close()
				}
				wait(t, done)
				if err == nil || !result.Attempted || !result.Ended || result.Acknowledged || !result.WriteOutcomeUnknown || calls.Load() != 1 {
					t.Fatal("original applied write was confirmed or replayed", err)
				}
				if mode == "partial" && (len(result.Body) == 0 || result.Captured || result.EOF) {
					t.Fatal("complete native body without original EOF confirmed")
				}
				if mode == "http500" || mode == "http408" {
					if _, err := c.Execute(context.Background(), earlier); !errors.Is(err, ErrWriteUnknown) {
						t.Fatal("prepared later request bypassed unknown barrier", err)
					}
					nativeDo(t, c, Request{Method: "GET", Path: "/v1/session/list"})
					unknown, ok := c.UnknownResponse()
					if !ok || unknown.Request.Path != request.Path || unknown.Status != result.Status || !bytes.Equal(unknown.Body, result.Body) || calls.Load() != 2 {
						t.Fatal("later original read cleared unknown")
					}
				}
				switch kind {
				case "delete":
					if nativeDo(t, base, readRequest(key)).Status != 404 {
						t.Fatal("actual original delete was not applied")
					}
				case "txn":
					if !bytes.Equal(nativeRead(t, base, key).Value, value) {
						t.Fatal("actual original transaction was not applied")
					}
				case "session-create":
					list := nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/list"})
					var sessions []struct{ ID, Name string }
					if json.Unmarshal(list.Body, &sessions) != nil {
						t.Fatal("actual session list invalid")
					}
					found := 0
					for _, session := range sessions {
						if session.Name == t.Name() {
							found++
							id = session.ID
						}
					}
					if found != 1 || !validID(id) {
						t.Fatal("actual session creation was lost or replayed")
					}
				case "session-destroy", "session-renew":
					info := nativeDo(t, base, Request{Method: "GET", Path: "/v1/session/info/" + id})
					var sessions []struct{ ID string }
					if json.Unmarshal(info.Body, &sessions) != nil || kind == "session-destroy" && len(sessions) != 0 || kind == "session-renew" && (len(sessions) != 1 || sessions[0].ID != id) {
						t.Fatal("original session operation changed identity")
					}
				}
				if kind == "session-create" || kind == "session-renew" {
					nativeDo(t, base, Request{Method: "PUT", Path: "/v1/session/destroy/" + id})
				}
			})
		}
	}
}

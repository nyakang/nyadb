package qdrant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQdrantManagementNativePlansCaptureExactRequestsAndOneUseAcrossClients(t *testing.T) {
	config := []byte(`{"vectors":{"size":2,"distance":"Dot"},"metadata":{"n":9007199254740993,"d":1.2300e-20,"text":"猫🐱"}}`)
	p, err := PrepareCollectionManagement("owned 猫", "collection_create", config, 17)
	if err != nil {
		t.Fatal(err)
	}
	config[1] = 'x'
	preview := p.Preview()
	if preview.Method != "PUT" || preview.Path != "/collections/owned%20%E7%8C%AB?timeout=17" || !strings.Contains(string(preview.Body), "9007199254740993") || !strings.Contains(string(preview.Body), "1.2300e-20") {
		t.Fatal("native JSON or qualified request changed")
	}
	preview.Body[1] = 'x'
	var calls atomic.Int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		if unitHandshake(w, r) {
			return
		}
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(p.Preview().Body) || r.URL.Query().Get("timeout") != "17" {
			t.Error("captured native body or commit timeout changed")
		}
		fmt.Fprint(w, `{"status":"ok","result":true}`)
	}
	c, another := unitClient(t, handler), unitClient(t, handler)
	r, err := c.SubmitManagement(context.Background(), p)
	if err != nil || !r.HasResult || !r.Result || r.Response.Status != 200 {
		t.Fatal("native boolean receipt lost", err)
	}
	if _, err = another.SubmitManagement(context.Background(), p); !errors.Is(err, ErrManagementUsed) || calls.Load() != 1 {
		t.Fatal("copied original plan replayed on another executor", err)
	}
	changes := []AliasChange{{Action: "delete_alias", Alias: "original"}, {Action: "create_alias", Alias: "original", Collection: "replacement"}, {Action: "rename_alias", Alias: "original", Destination: "new"}}
	alias, err := PrepareAliasManagement(changes, 3)
	if err != nil {
		t.Fatal(err)
	}
	changes[1].Collection = "elsewhere"
	if string(alias.Preview().Body) != `{"actions":[{"delete_alias":{"alias_name":"original"}},{"create_alias":{"alias_name":"original","collection_name":"replacement"}},{"rename_alias":{"new_alias_name":"new","old_alias_name":"original"}}]}` {
		t.Fatal("ordered native alias actions changed")
	}
}

func TestQdrantManagementNativeRejectsInvalidPlansAndPreservesFalseFailureReceipts(t *testing.T) {
	for _, input := range []struct {
		name, action, raw string
		timeout           int
	}{
		{"../other", "collection_create", `{}`, 2}, {"owned", "collection_create", `{}`, 0}, {"owned", "collection_create", `{}`, 301},
		{"owned", "create", `{}`, 2}, {"owned", "collection_delete", `{}`, 2}, {"owned", "collection_update", `{}`, 2},
		{"owned", "collection_update", `{"unsupported":true}`, 2}, {"owned", "collection_create", `{"vectors":{},"vectors":{}}`, 2},
		{"owned", "collection_create", `{"metadata":{"n":NaN}}`, 2}, {"owned", "collection_create", `{"metadata":{"text":"\ud800"}}`, 2},
	} {
		if _, err := PrepareCollectionManagement(input.name, input.action, []byte(input.raw), input.timeout); err == nil {
			t.Fatal("invalid native configuration accepted", input)
		}
	}
	for _, changes := range [][]AliasChange{nil, {{Action: "unknown", Alias: "a"}}, {{Action: "create_alias", Alias: "a", Collection: "../b"}}, {{Action: "delete_alias", Alias: "a", Collection: "b"}}, {{Action: "rename_alias", Alias: "a", Destination: "a"}}, {{Action: "rename_alias", Alias: "a", Destination: "b", Collection: "c"}}} {
		if _, err := PrepareAliasManagement(changes, 2); err == nil {
			t.Fatal("invalid native alias operation accepted")
		}
	}
	for _, body := range []string{`{"status":"ok","result":false}`, `{"status":"ok","result":{"operation_id":1}}`, `{"status":"ok","result":true`, "native denial\xff"} {
		t.Run(body, func(t *testing.T) {
			var calls atomic.Int32
			c := unitClient(t, func(w http.ResponseWriter, r *http.Request) {
				if unitHandshake(w, r) {
					return
				}
				calls.Add(1)
				if strings.HasPrefix(body, "native denial") {
					w.WriteHeader(403)
				}
				fmt.Fprint(w, body)
			})
			p, _ := PrepareCollectionManagement("owned", "collection_delete", nil, 2)
			r, err := c.SubmitManagement(context.Background(), p)
			if string(r.Response.Body) != body || r.Result {
				t.Fatal("original native response was replaced")
			}
			if strings.Contains(body, "false") {
				if err != nil || !r.HasResult {
					t.Fatal("false boolean is not a settled native result", err)
				}
			} else {
				var interrupted *Interrupted
				if !errors.As(err, &interrupted) || !interrupted.WriteOutcomeUnknown || r.HasResult {
					t.Fatal("invalid or failed native receipt became success", err)
				}
			}
			if _, err = c.SubmitManagement(context.Background(), p); !errors.Is(err, ErrManagementUsed) || calls.Load() != 1 {
				t.Fatal("native false/failure plan replayed", err)
			}
		})
	}
}

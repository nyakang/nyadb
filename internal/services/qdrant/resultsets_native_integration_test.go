package qdrant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

func TestQdrantNativeSessionTypedPointsAndExactRawEnvelopes(t *testing.T) {
	s, _, _ := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	nativeUpsert(t, c, name, `{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"integer":9007199254740993,"decimal":1.2300e-20,"text":"原生🐱","nil":null,"flag":true,"nested":{"n":9007199254740995}}},{"id":9007199254740993,"vector":[0.5,0.5],"payload":{"text":"second"}},{"id":"550e8400-e29b-41d4-a716-446655440000","vector":[0,1],"payload":{"text":"uuid"}}]}`)
	l, _ := s.ReserveQuery(1)
	for _, source := range []string{
		"POST /collections/" + name + `/points/scroll
{"limit":2,"with_payload":true,"with_vector":true}`,
		"POST /collections/" + name + `/points/query
{"query":[1,0],"limit":3,"with_payload":true,"with_vector":true}`,
		"POST /collections/" + name + `/points
{"ids":[18446744073709551615,9007199254740993,"550e8400-e29b-41d4-a716-446655440000"],"with_payload":true,"with_vector":true}`,
		"GET /collections/" + name + "/points/18446744073709551615?with_payload=true&with_vector=true",
		"GET /collections/" + name + "/points/550e8400-e29b-41d4-a716-446655440000?with_payload=true&with_vector=true",
		"POST /collections/" + name + `/points/search
{"vector":[1,0],"limit":3,"with_payload":true,"with_vector":true}`,
		"POST /collections/" + name + `/points/recommend
{"positive":[9007199254740993],"limit":3,"with_payload":true,"with_vector":true}`,
		"POST /collections/" + name + `/points/discover
{"target":[1,0],"limit":3,"with_payload":true,"with_vector":true}`,
	} {
		result, err := l.Execute(context.Background(), name, source)
		if err != nil || len(result.Sets) != 2 || len(result.Sets[0].Rows) == 0 || result.Steps[0].State != "completed" || result.Truncated || result.AffectedRows != 0 {
			t.Fatal("real point route, typed result and receipt", source, result, err)
		}
		raw := result.Sets[1].Rows[0][1]
		if raw.Kind != contract.JSON || result.Sets[1].Rows[0][0].Text != "200" || !strings.Contains(raw.Text, `"result"`) {
			t.Fatal("native evidence envelope lost")
		}
		for i := range result.Sets[0].Rows {
			cells := pointCells(t, result.Sets[0], i)
			if _, flattened := cells["payload"]; flattened || cells["vector"].Kind != contract.JSON || cells["text"].Kind != contract.Text {
				t.Fatal("native vector or payload not typed", source, cells)
			}
			if cells["id"].Text == "18446744073709551615" && (cells["id"].Kind != contract.Integer || cells["integer"].Text != "9007199254740993" || cells["decimal"].Kind != contract.Decimal || cells["nil"].Kind != contract.Null || cells["flag"].Kind != contract.Boolean || cells["nested"].Text != `{"n":9007199254740995}`) {
				t.Fatal("native uint64/payload precision or types lost", cells)
			}
		}
		if strings.Contains(source, "/points/scroll") && !strings.Contains(raw.Text, `"next_page_offset"`) {
			t.Fatal("native cursor discarded")
		}
	}
	result, err := l.Execute(context.Background(), name, "POST /collections/"+name+"/points/count\n{\"exact\":true}")
	if err != nil || len(result.Sets) != 2 || result.Sets[0].Columns[0].Name != "count" || result.Sets[0].Rows[0][0].Kind != contract.Integer || result.Sets[0].Rows[0][0].Text != "3" || result.AffectedRows != 0 {
		t.Fatal("native count typed or mistaken for affected rows", result, err)
	}
	result, err = l.Execute(context.Background(), name, "POST /collections/"+name+`/points/query
{"query":[1,0],"filter":{"must":[{"key":"text","match":{"value":"absent"}}]},"limit":3}`)
	if err != nil || len(result.Sets) != 2 || len(result.Sets[0].Rows) != 0 || result.Sets[0].Columns[0].Name != "id" {
		t.Fatal("empty native query invented rows", err)
	}
	// Confirm a real server read rejection preserves the same original lease.
	result, err = l.Execute(context.Background(), name, "POST /collections/"+name+"/points/query\n{\"query\":[1,0,1],\"limit\":1}")
	if err == nil || errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "failed" || result.Truncated || len(result.Sets) != 1 || result.Sets[0].Rows[0][0].Text != "400" {
		t.Fatal("real read rejection claimed uncertain mutation/lost session", result, err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); err != nil {
		t.Fatal("read rejection retired original lease", err)
	}
	count, _, err := c.Count(context.Background(), name, nil)
	if err != nil || count != 3 {
		t.Fatal("native read classification mutated points", err)
	}

	collision := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	nativeUpsert(t, c, collision, `{"points":[{"id":1,"vector":[1,0],"payload":{"score":"business","id":"business_id","x":2}}]}`)
	result, err = l.Execute(context.Background(), collision, "POST /collections/"+collision+`/points/query
{"query":[1,0],"with_payload":true,"limit":1}`)
	if err != nil || len(result.Sets) != 2 {
		t.Fatal("native payload collision", err)
	}
	cells := pointCells(t, result.Sets[0], 0)
	if cells["id"].Text != "1" || cells["score"].Kind == contract.Text || cells["payload"].Kind != contract.JSON || !strings.Contains(cells["payload"].Text, `"business_id"`) {
		t.Fatal("native reserved fields overwritten by business payload", cells)
	}

	named := nativeCollection(t, c, `{"vectors":{"dense":{"size":2,"distance":"Dot"},"multi":{"size":2,"distance":"Dot","multivector_config":{"comparator":"max_sim"}}},"sparse_vectors":{"sparse":{}}}`)
	// The foundation separately verifies Qdrant 1.20's ambiguous integer
	// matrix rejection. Use an unambiguous native multidense fixture here.
	nativeUpsert(t, c, named, `{"points":[{"id":1,"vector":{"dense":[1,0],"multi":[[0.75,0.25],[0.25,0.75]],"sparse":{"indices":[2,8],"values":[0.25,0.5]}},"payload":{"text":"named"}}]}`)
	result, err = l.Execute(context.Background(), named, "POST /collections/"+named+`/points/query
{"query":[1,0],"using":"dense","with_payload":true,"with_vector":true,"limit":1}`)
	if err != nil || len(result.Sets) != 2 {
		t.Fatal("native named vector result", err)
	}
	cells = pointCells(t, result.Sets[0], 0)
	if cells["vector"].Kind != contract.JSON || !strings.Contains(cells["vector"].Text, `"multi"`) || !strings.Contains(cells["vector"].Text, `"sparse"`) || !strings.Contains(cells["vector"].Text, `"dense"`) {
		t.Fatal("typed result discarded named/multi/sparse vectors")
	}
}

func TestQdrantNativeSessionTypedPostReadLossIsInterruptedWithoutReplay(t *testing.T) {
	_, _, secret := nativeSession(t)
	c := nativeOpen(t)
	name := nativeCollection(t, c, `{"vectors":{"size":2,"distance":"Dot"}}`)
	nativeUpsert(t, c, name, `{"points":[{"id":18446744073709551615,"vector":[1,0],"payload":{"text":"original"}}]}`)
	cfg, calls := nativeFaultProxy(t, c, func(r *http.Request) bool {
		return r.Method == "POST" && r.URL.Path == "/collections/"+name+"/points/query"
	}, func(w http.ResponseWriter, r *http.Request, response Response) {
		if !bytes.Contains(response.Body, []byte("18446744073709551615")) {
			t.Error("fault did not capture original native point read")
		}
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buffer, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 99999\r\n\r\n")
		buffer.Write(response.Body[:len(response.Body)/2])
		buffer.Flush()
	})
	s := testSession(t, cfg, secret)
	l, _ := s.ReserveQuery(1)
	result, err := l.Execute(context.Background(), name, "POST /collections/"+name+`/points/query
{"query":[1,0],"with_payload":true,"limit":1}`)
	if !errors.Is(err, contract.ErrSQLSessionLost) || result.Steps[0].State != "interrupted" || !result.Truncated || len(result.Sets) != 1 || !result.Sets[0].Rows[0][1].Truncated {
		t.Fatal("native lost POST read classified as uncertain mutation", result, err)
	}
	if _, err := l.Execute(context.Background(), "", "GET /"); !errors.Is(err, contract.ErrSQLSessionLost) || calls.Load() != 1 {
		t.Fatal("lost original read replayed", err)
	}
	replacement, err := l.Replace()
	if err != nil {
		t.Fatal("explicit original recovery", err)
	}
	if _, err := replacement.Execute(context.Background(), "", "GET /"); err != nil || calls.Load() != 1 {
		t.Fatal("recovery replayed original POST", err)
	}
	count, _, err := c.Count(context.Background(), name, nil)
	if err != nil || count != 1 {
		t.Fatal("native read fault changed points", err)
	}
}

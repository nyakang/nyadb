package meilisearch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMeilisearchDocumentsPageBoundariesAndExactSource(t *testing.T) {
	source := `{"results":[{"id":9007199254740993,"n":9007199254740993,"future":{"text":"原生🐱"}}],"offset":0,"limit":2,"total":1,"unknown":true}`
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("offset") != "0" || r.URL.Query().Get("limit") != "2" {
			t.Error("wrong original page request")
		}
		_, _ = io.WriteString(w, source)
	})
	page, response, err := c.ListDocuments(context.Background(), "owned", DocumentsRequest{Limit: 2})
	if err != nil || page.Offset != 0 || page.Total != 1 || page.Limit != 2 || len(page.Documents) != 1 || string(page.Raw) != source || !bytes.Contains(page.Documents[0], []byte("9007199254740993")) {
		t.Fatal(page, err)
	}
	response.Body[0] = '!'
	if string(page.Raw) != source {
		t.Fatal("page aliases transport response")
	}
	page.Raw[0] = '!'
	if !bytes.Contains(page.Documents[0], []byte("原生🐱")) {
		t.Fatal("document source aliases envelope")
	}
}
func TestMeilisearchDocumentsRejectMalformedPagesAndKeepNativeBounds(t *testing.T) {
	for _, source := range []string{`{"results":null,"offset":0,"limit":2,"total":0}`, `{"results":[],"offset":1,"limit":2,"total":0}`, `{"results":[],"offset":0,"limit":1,"total":0}`, `{"results":[],"offset":0,"limit":2,"total":3}`, `{"results":[1],"offset":0,"limit":2,"total":1}`, `{"results":[],"offset":0,"limit":2,"total":1.0}`, `{"results":[],"offset":0,"limit":2,"total":9007199254740993.0}`} {
		t.Run(source, func(t *testing.T) {
			c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, source) })
			_, response, err := c.ListDocuments(context.Background(), "owned", DocumentsRequest{Limit: 2})
			if err == nil || string(response.Body) != source || errors.Is(err, ErrRetired) {
				t.Fatal("invalid original envelope accepted or erased", err)
			}
		})
	}
	for _, request := range []DocumentsRequest{{Offset: 100, Limit: 2}, {Limit: 0}} {
		c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"results":[],"offset":`+r.URL.Query().Get("offset")+`,"limit":`+r.URL.Query().Get("limit")+`,"total":4}`)
		})
		page, _, err := c.ListDocuments(context.Background(), "owned", request)
		if err != nil || page.Total != 4 || len(page.Documents) != 0 {
			t.Fatal("native empty/count boundary lost", err)
		}
	}
}
func TestMeilisearchDocumentIDsBoundsAndReadonlyErrors(t *testing.T) {
	var calls atomic.Int32
	c, _ := mockOpen(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"code":"document_not_found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":9007199254740993,"n":9007199254740993}`)
	})
	for _, id := range []string{"", strings.Repeat("a", 512), "a/b", "a%2Fb", "a b", "原生", "1.0", "a?b", "a#b", "a\x00b"} {
		if _, _, err := c.ReadDocument(context.Background(), "owned", id); err == nil {
			t.Fatal("invalid native ID accepted")
		}
	}
	for _, request := range []DocumentsRequest{{Limit: -1}, {Limit: 1001}, {Offset: math.MaxUint64}} {
		if _, _, err := c.ListDocuments(context.Background(), "owned", request); !errors.Is(err, ErrBounds) {
			t.Fatal("unbounded enumeration", err)
		}
	}
	c.config.IndexUID = "owned"
	if _, _, err := c.ReadDocument(context.Background(), "other", "one"); err == nil {
		t.Fatal("foreign index admitted")
	}
	if _, _, err := c.ListDocuments(context.Background(), "other", DocumentsRequest{Limit: 2}); err == nil {
		t.Fatal("foreign enumeration admitted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid typed request reached transport")
	}
	_, response, err := c.ReadDocument(context.Background(), "owned", "missing")
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 404 || errors.Is(err, ErrRetired) || !bytes.Contains(response.Body, []byte("document_not_found")) {
		t.Fatal(err)
	}
	raw, _, err := c.ReadDocument(context.Background(), "owned", "9007199254740993")
	if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) || calls.Load() != 2 {
		t.Fatal("complete error retired original client", err)
	}
}

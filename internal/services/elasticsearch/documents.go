package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DocumentID includes routing because equal index/ID values on different shards
// can refer to different documents. Editing always uses the captured identity.
type DocumentID struct {
	Index, ID, Routing string
}

type Snapshot struct {
	Identity    DocumentID
	Sequence    int64
	PrimaryTerm int64
	Source      json.RawMessage
}

func (s Snapshot) Clone() Snapshot { s.Source = bytes.Clone(s.Source); return s }

type MutationResult struct {
	Snapshot Snapshot
	Result   string
	Response Response
}

func validIndex(s string) bool {
	return len(s) > 0 && len(s) <= 255 && utf8.ValidString(s) && s != "." && s != ".." && s[0] != '_' && s[0] != '-' && s[0] != '+' && strings.ToLower(s) == s && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0 && !strings.ContainsAny(s, `\/*?"<>|,#:`)
}

func validDocumentID(id DocumentID) bool {
	return validIndex(id.Index) && len(id.ID) > 0 && len(id.ID) <= 512 && len(id.Routing) <= 512 && utf8.ValidString(id.ID) && utf8.ValidString(id.Routing) && strings.IndexFunc(id.ID+id.Routing, unicode.IsControl) < 0 && id.ID != "." && id.ID != ".."
}

func documentPath(id DocumentID, action string, version *Snapshot) string {
	path := "/" + url.PathEscape(id.Index) + "/" + action + "/" + url.PathEscape(id.ID)
	query := url.Values{}
	if id.Routing != "" {
		query.Set("routing", id.Routing)
	}
	if version != nil {
		query.Set("if_seq_no", strconv.FormatInt(version.Sequence, 10))
		query.Set("if_primary_term", strconv.FormatInt(version.PrimaryTerm, 10))
	}
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	return path
}

func validSource(raw []byte) bool {
	if len(raw) > 1<<20 || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return false
	}
	var object map[string]json.RawMessage
	return decodeJSON(raw, &object) == nil && object != nil
}

type nativeDocument struct {
	Index       string          `json:"_index"`
	ID          string          `json:"_id"`
	Routing     string          `json:"_routing"`
	Sequence    *int64          `json:"_seq_no"`
	PrimaryTerm *int64          `json:"_primary_term"`
	Source      json.RawMessage `json:"_source"`
	Found       *bool           `json:"found"`
	Result      string          `json:"result"`
	Shards      struct {
		Total      int `json:"total"`
		Successful int `json:"successful"`
		Failed     int `json:"failed"`
	} `json:"_shards"`
}

func (n nativeDocument) snapshot(id DocumentID) (Snapshot, error) {
	if n.Index != id.Index || n.ID != id.ID || n.Sequence == nil || *n.Sequence < 0 || n.PrimaryTerm == nil || *n.PrimaryTerm <= 0 {
		return Snapshot{}, ErrProtocol
	}
	return Snapshot{Identity: id, Sequence: *n.Sequence, PrimaryTerm: *n.PrimaryTerm, Source: bytes.Clone(n.Source)}, nil
}

func (c *Client) GetDocument(ctx context.Context, id DocumentID) (Snapshot, error) {
	if !validDocumentID(id) {
		return Snapshot{}, ErrInvalid
	}
	if !c.operation.TryLock() {
		return Snapshot{}, ErrBusy
	}
	defer c.operation.Unlock()
	if err := c.available(); err != nil {
		return Snapshot{}, err
	}
	// Typed components are encoded independently. An escaped slash inside an
	// ID is data, and must not be parsed as raw REST path traversal.
	r, err := c.perform(ctx, Request{Method: "GET", Path: documentPath(id, "_doc", nil)})
	if err != nil {
		return Snapshot{}, err
	}
	var document nativeDocument
	if decodeJSON(r.Body, &document) != nil || document.Found == nil || !*document.Found || !validSource(document.Source) || document.Routing != "" && document.Routing != id.Routing {
		return Snapshot{}, ErrProtocol
	}
	return document.snapshot(id)
}

func (c *Client) CreateDocument(ctx context.Context, id DocumentID, source json.RawMessage) (MutationResult, error) {
	return c.mutateDocument(ctx, "PUT", "_create", id, nil, source, mutationOptions{}, "created")
}

// ReplaceDocument requires both original native concurrency tokens. It does
// not fetch a newer version or retry a conflict behind the user's back.
func (c *Client) ReplaceDocument(ctx context.Context, original Snapshot, source json.RawMessage) (MutationResult, error) {
	return c.mutateDocument(ctx, "PUT", "_doc", original.Identity, &original, source, mutationOptions{}, "updated", "noop")
}

func (c *Client) DeleteDocument(ctx context.Context, original Snapshot) (MutationResult, error) {
	return c.mutateDocument(ctx, "DELETE", "_doc", original.Identity, &original, nil, mutationOptions{}, "deleted")
}

type mutationOptions struct {
	Target                                   string
	WaitRefresh, RequireAlias, RequireStream bool
}

func (c *Client) mutateDocument(ctx context.Context, method, action string, id DocumentID, original *Snapshot, source json.RawMessage, options mutationOptions, accepted ...string) (MutationResult, error) {
	var result MutationResult
	if !validDocumentID(id) || method != "DELETE" && !validSource(source) || original != nil && (original.Sequence < 0 || original.PrimaryTerm <= 0) {
		return result, ErrInvalid
	}
	request := Request{Method: method, Path: documentPath(id, action, original), Body: bytes.Clone(source)}
	if options.Target != "" {
		if !validIndex(options.Target) || method == "DELETE" || original != nil {
			return result, ErrInvalid
		}
		target := id
		target.Index = options.Target
		request.Path = documentPath(target, action, original)
	}
	parsed, _ := url.Parse(request.Path)
	query := parsed.Query()
	query.Set("include_source_on_error", "false")
	if method == "DELETE" {
		query.Del("include_source_on_error")
	}
	if options.WaitRefresh {
		query.Set("refresh", "wait_for")
	}
	if options.RequireAlias {
		query.Set("require_alias", "true")
	}
	if options.RequireStream {
		query.Set("require_data_stream", "true")
	}
	parsed.RawQuery = query.Encode()
	request.Path = parsed.String()
	if !c.operation.TryLock() {
		return result, ErrBusy
	}
	defer c.operation.Unlock()
	if err := c.available(); err != nil {
		return result, err
	}
	r, err := c.perform(ctx, request)
	result.Response = r
	if err != nil {
		return result, err
	}
	var native nativeDocument
	if decodeJSON(r.Body, &native) == nil {
		result.Snapshot, err = native.snapshot(id)
		result.Snapshot.Source = bytes.Clone(source)
		result.Result = native.Result
	} else {
		err = ErrProtocol
	}
	known := false
	for _, value := range accepted {
		known = known || native.Result == value
	}
	if err != nil || !known || native.Shards.Total <= 0 || native.Shards.Successful <= 0 || native.Shards.Failed != 0 {
		c.mu.Lock()
		c.retired = true
		c.mu.Unlock()
		return result, &Interrupted{WriteOutcomeUnknown: true, Cause: errors.New("Elasticsearch write receipt is incomplete; verify the original document")}
	}
	return result, nil
}

package consul

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const HTTPRequestMethod = contract.KVConsulHTTPRequestMethod
const projectionBudget = 32 << 20

type requestEnvelope struct {
	Method string     `json:"method"`
	Path   string     `json:"path"`
	Query  url.Values `json:"query,omitempty"`
	Body   *string    `json:"body_base64,omitempty"`
}

// DocumentRequest captures the exact native request without headers or secrets.
func DocumentRequest(r Request) (contract.KVRequest, error) {
	if _, _, _, err := validateRequest(r); err != nil {
		return contract.KVRequest{}, err
	}
	e := requestEnvelope{Method: r.Method, Path: r.Path, Query: r.Query}
	if r.Body != nil {
		v := base64.StdEncoding.EncodeToString(r.Body)
		e.Body = &v
	}
	raw, err := json.MarshalIndent(e, "", "  ")
	if err != nil || len(raw) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	return contract.KVRequest{Method: HTTPRequestMethod, JSON: string(raw)}, nil
}

func decodeDocumentRequest(r contract.KVRequest) (Request, contract.KVRequest, bool, error) {
	if r.Method != HTTPRequestMethod {
		return Request{}, r, false, contract.ErrUnsupported
	}
	var fields map[string]json.RawMessage
	var e requestEnvelope
	raw := []byte(r.JSON)
	if jsondocument.Decode(raw, &fields, requestLimit) != nil || fields == nil || json.Unmarshal(raw, &e) != nil {
		return Request{}, r, false, ErrInvalid
	}
	for key, value := range fields {
		if key != "method" && key != "path" && key != "query" && key != "body_base64" || string(value) == "null" {
			return Request{}, r, false, ErrInvalid
		}
	}
	// encoding/json accepts null as an empty Go string. Query values must be
	// authored strings; null must never become an enabled native flag.
	if rawQuery, present := fields["query"]; present {
		var query map[string][]json.RawMessage
		if json.Unmarshal(rawQuery, &query) != nil {
			return Request{}, r, false, ErrInvalid
		}
		for _, values := range query {
			for _, value := range values {
				v := bytes.TrimSpace(value)
				if len(v) == 0 || v[0] != '"' {
					return Request{}, r, false, ErrInvalid
				}
			}
		}
	}
	native := Request{Method: e.Method, Path: e.Path, Query: e.Query}
	if e.Body != nil {
		b, err := base64.StdEncoding.Strict().DecodeString(*e.Body)
		if err != nil || base64.StdEncoding.EncodeToString(b) != *e.Body {
			return Request{}, r, false, ErrInvalid
		}
		native.Body = b
	}
	_, write, _, err := validateRequest(native)
	if err != nil {
		return Request{}, r, false, err
	}
	canonical, err := DocumentRequest(native)
	return native, canonical, write, err
}

func nativeEntryCost(e Entry) int64 {
	n := int64(128 + len(e.Key) + len(e.Value) + len(e.Session))
	for _, field := range e.PresentFields {
		n += int64(16 + len(field))
	}
	return n
}
func neutralEntry(e Entry) contract.KVHTTPEntry {
	return contract.KVHTTPEntry{PresentFields: e.PresentFields, Key: e.Key, Value: e.Value,
		ValuePresent: e.ValuePresent, ValueNull: e.ValueNull, Flags: e.Flags,
		CreateIndex: e.CreateIndex, ModifyIndex: e.ModifyIndex, LockIndex: e.LockIndex, Session: e.Session}
}

// Optional typed/JSON projections cannot replace or invalidate the original
// HTTP body and native ACK. The body is never called protobuf data.
func documentResult(request contract.KVRequest, r Response) contract.KVResult {
	h := &contract.KVHTTPResult{URL: r.URL, Request: contract.KVHTTPRequest{Method: r.Request.Method, Path: r.Request.Path, Query: r.Request.Query, Body: r.Request.Body},
		Kind: r.Kind, Protocol: r.Protocol, Attempted: r.Attempted, Ended: r.Ended, Captured: r.Captured, EOF: r.EOF,
		Acknowledged: r.Acknowledged, Succeeded: r.Succeeded, WriteOutcomeUnknown: r.WriteOutcomeUnknown, Canceled: r.Canceled,
		Status: r.Status, Header: r.Header, Trailer: r.Trailer, Body: r.Body, SessionID: r.SessionID,
		CaptureError: r.CaptureError, ProjectionError: r.ProjectionError,
		Metadata: contract.KVHTTPMetadata{Index: r.Metadata.Index, LastContact: r.Metadata.LastContact,
			KnownLeader: r.Metadata.KnownLeader, ACLFiltered: r.Metadata.ACLFiltered,
			QueryBackend: r.Metadata.QueryBackend, DefaultACLPolicy: r.Metadata.DefaultACLPolicy}}
	out := contract.KVResult{Request: request, Attempted: r.Attempted, Completed: r.Acknowledged,
		WriteOutcomeUnknown: r.WriteOutcomeUnknown, Code: fmt.Sprintf("HTTP %d", r.Status), HTTP: h, ProjectionError: r.ProjectionError}
	if r.Status == 0 {
		out.Code = "HTTP unavailable"
	}
	cost := int64(0)
	for _, e := range r.Entries {
		cost += nativeEntryCost(e)
	}
	if r.Txn != nil {
		for _, e := range r.Txn.Results {
			cost += nativeEntryCost(e)
		}
		for _, e := range r.Txn.Errors {
			cost += int64(32 + len(e.What))
		}
	}
	if cost <= projectionBudget {
		for _, e := range r.Entries {
			h.Entries = append(h.Entries, neutralEntry(e))
		}
		if r.Txn != nil {
			h.Txn = &contract.KVHTTPTransaction{Committed: r.Txn.Committed}
			for _, e := range r.Txn.Results {
				h.Txn.Results = append(h.Txn.Results, neutralEntry(e))
			}
			for _, e := range r.Txn.Errors {
				h.Txn.Errors = append(h.Txn.Errors, contract.KVHTTPTransactionError{OpIndex: e.OpIndex, What: e.What})
			}
		}
	} else {
		out.ProjectionError = "typed HTTP projection exceeds 32 MiB; original body and acknowledgement retained"
		h.ProjectionError = out.ProjectionError
	}
	if len(r.Body) <= 4<<20 && r.Captured {
		var v any
		if jsondocument.Decode(r.Body, &v, 4<<20) == nil {
			out.JSON = string(r.Body)
		}
	}
	return out
}

func resultCost(r *contract.KVResult) int64 {
	if r == nil {
		return 0
	}
	n := int64(1<<20) + int64(len(r.Request.JSON)+len(r.JSON)+len(r.Native)+len(r.Code)+len(r.ProjectionError))
	if h := r.HTTP; h != nil {
		n += int64(len(h.Body) + len(h.Request.Body) + len(h.Request.Method) + len(h.Request.Path) + len(h.URL) + len(h.CaptureError) + len(h.ProjectionError))
		for _, fields := range []map[string][]string{h.Header, h.Trailer, h.Request.Query} {
			for key, values := range fields {
				n += int64(64 + len(key))
				for _, v := range values {
					n += int64(24 + len(v))
				}
			}
		}
		for _, e := range h.Entries {
			n += neutralEntryCost(e)
		}
		if h.Txn != nil {
			for _, e := range h.Txn.Results {
				n += neutralEntryCost(e)
			}
			for _, e := range h.Txn.Errors {
				n += int64(32 + len(e.What))
			}
		}
	}
	return n
}
func neutralEntryCost(e contract.KVHTTPEntry) int64 {
	n := int64(128 + len(e.Key) + len(e.Value) + len(e.Session))
	for _, f := range e.PresentFields {
		n += int64(16 + len(f))
	}
	return n
}

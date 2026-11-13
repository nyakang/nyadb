package kvworkspace

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type httpEntryRecord struct {
	PresentFields                              []string
	Key                                        string
	Value                                      []byte
	ValuePresent, ValueNull                    bool
	Flags, CreateIndex, ModifyIndex, LockIndex string
	Session                                    string
	SessionNull                                bool
}

func httpEntryData(e contract.KVHTTPEntry) httpEntryRecord {
	return httpEntryRecord{PresentFields: e.PresentFields, Key: e.Key, Value: e.Value, ValuePresent: e.ValuePresent, ValueNull: e.ValueNull,
		Flags: strconv.FormatUint(e.Flags, 10), CreateIndex: strconv.FormatUint(e.CreateIndex, 10), ModifyIndex: strconv.FormatUint(e.ModifyIndex, 10), LockIndex: strconv.FormatUint(e.LockIndex, 10), Session: e.Session, SessionNull: e.SessionNull}
}
func uintRecord(value *uint64) *string {
	if value == nil {
		return nil
	}
	v := strconv.FormatUint(*value, 10)
	return &v
}
func encodeHTTPOperationReceipt(r Receipt) ([]byte, error) {
	h := r.Result.HTTP
	if h == nil {
		return nil, jsondocument.ErrInvalid
	}
	type scopeRecord struct {
		ProfileID, Generation, Catalog, Schema, PrefixBase64, Version string
		HTTP                                                          contract.KVHTTPIdentity
	}
	type metadataRecord struct {
		Index, LastContact             *string
		KnownLeader, ACLFiltered       *bool
		QueryBackend, DefaultACLPolicy string
	}
	type transactionRecord struct {
		Committed bool
		Results   []httpEntryRecord
		Errors    []contract.KVHTTPTransactionError
	}
	type httpRecord struct {
		URL                                                                                     string
		Request                                                                                 contract.KVHTTPRequest
		Kind, Protocol                                                                          string
		Attempted, Ended, Captured, EOF, Acknowledged, Succeeded, WriteOutcomeUnknown, Canceled bool
		Status                                                                                  int
		Header, Trailer                                                                         map[string][]string
		Body                                                                                    []byte
		Metadata                                                                                metadataRecord
		Entries                                                                                 []httpEntryRecord
		Txn                                                                                     *transactionRecord
		SessionID, CaptureError, ProjectionError                                                string
	}
	http := httpRecord{URL: h.URL, Request: h.Request, Kind: h.Kind, Protocol: h.Protocol, Attempted: h.Attempted, Ended: h.Ended, Captured: h.Captured, EOF: h.EOF,
		Acknowledged: h.Acknowledged, Succeeded: h.Succeeded, WriteOutcomeUnknown: h.WriteOutcomeUnknown, Canceled: h.Canceled,
		Status: h.Status, Header: h.Header, Trailer: h.Trailer, Body: h.Body, SessionID: h.SessionID, CaptureError: h.CaptureError, ProjectionError: h.ProjectionError,
		Metadata: metadataRecord{Index: uintRecord(h.Metadata.Index), LastContact: uintRecord(h.Metadata.LastContact), KnownLeader: h.Metadata.KnownLeader,
			ACLFiltered: h.Metadata.ACLFiltered, QueryBackend: h.Metadata.QueryBackend, DefaultACLPolicy: h.Metadata.DefaultACLPolicy}}
	for _, e := range h.Entries {
		http.Entries = append(http.Entries, httpEntryData(e))
	}
	if h.Txn != nil {
		http.Txn = &transactionRecord{Committed: h.Txn.Committed, Errors: h.Txn.Errors}
		for _, e := range h.Txn.Results {
			http.Txn.Results = append(http.Txn.Results, httpEntryData(e))
		}
	}
	record := struct {
		Version             int                     `json:"version"`
		Protocol            string                  `json:"protocol"`
		Scope               scopeRecord             `json:"scope"`
		Method              string                  `json:"method"`
		AuthoredJSON        string                  `json:"authored_json"`
		CanonicalJSON       string                  `json:"canonical_json"`
		Attempted           bool                    `json:"attempted"`
		Completed           bool                    `json:"completed"`
		WriteOutcomeUnknown bool                    `json:"write_outcome_unknown"`
		Code                string                  `json:"code"`
		HTTP                httpRecord              `json:"http"`
		SessionHeldKeys     *HTTPSessionKeysReceipt `json:"session_held_keys,omitempty"`
		Error               string                  `json:"error,omitempty"`
	}{Version: 1, Protocol: r.Scope.HTTP.Protocol,
		Scope: scopeRecord{ProfileID: strconv.FormatInt(r.Scope.Namespace.ProfileID, 10), Generation: strconv.FormatUint(r.Scope.Generation, 10),
			Catalog: r.Scope.Namespace.Catalog, Schema: r.Scope.Namespace.Schema, PrefixBase64: base64.StdEncoding.EncodeToString(r.Scope.Prefix), Version: r.Scope.Version, HTTP: r.Scope.HTTP},
		Method: r.Result.Request.Method, AuthoredJSON: r.Authored.JSON, CanonicalJSON: r.Result.Request.JSON,
		Attempted: r.Result.Attempted, Completed: r.Result.Completed, WriteOutcomeUnknown: r.Result.WriteOutcomeUnknown, Code: r.Result.Code, HTTP: http, Error: r.Error, SessionHeldKeys: r.HTTPSessionKeys.clone()}
	raw, err := json.MarshalIndent(record, "", "  ")
	if len(raw) > 256<<20 {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}

func httpReceiptPreview(h *contract.KVHTTPResult) string {
	if h == nil {
		return ""
	}
	body := h.Body[:min(len(h.Body), previewLimit)]
	// Inspect at most one preview plus the bytes needed to finish its last rune.
	// Full body encoding is reserved for explicit copy/export operations.
	if len(body) < len(h.Body) {
		for back := 1; back < utf8.UTFMax && back <= len(body); back++ {
			start := len(body) - back
			if utf8.RuneStart(body[start]) {
				if !utf8.FullRune(h.Body[start:min(len(h.Body), start+utf8.UTFMax)]) {
					break
				}
				_, size := utf8.DecodeRune(h.Body[start:min(len(h.Body), start+utf8.UTFMax)])
				if start+size > len(body) {
					body = body[:start]
				}
				break
			}
		}
	}
	if utf8.Valid(body) {
		text := string(body)
		if len(body) < len(h.Body) {
			return text + "\n…"
		}
		return text
	}
	return clipped("Base64:\n" + base64.StdEncoding.EncodeToString(body))
}

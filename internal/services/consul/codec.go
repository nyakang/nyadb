package consul

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Missing headers stay unavailable. Metadata is independent of the native ACK.
type Metadata struct {
	Index, LastContact             *uint64
	KnownLeader, ACLFiltered       *bool
	QueryBackend, DefaultACLPolicy string
}

func (m Metadata) Clone() Metadata {
	if m.Index != nil {
		v := *m.Index
		m.Index = &v
	}
	if m.LastContact != nil {
		v := *m.LastContact
		m.LastContact = &v
	}
	if m.KnownLeader != nil {
		v := *m.KnownLeader
		m.KnownLeader = &v
	}
	if m.ACLFiltered != nil {
		v := *m.ACLFiltered
		m.ACLFiltered = &v
	}
	return m
}
func decodeMetadata(h http.Header) (Metadata, error) {
	m := Metadata{}
	for key, target := range map[string]**uint64{"X-Consul-Index": &m.Index, "X-Consul-Lastcontact": &m.LastContact} {
		values := h.Values(key)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || !exactUint(values[0]) {
			return m, ErrProtocol
		}
		n, _ := strconv.ParseUint(values[0], 10, 64)
		*target = &n
	}
	for key, target := range map[string]**bool{"X-Consul-Knownleader": &m.KnownLeader, "X-Consul-Results-Filtered-By-Acls": &m.ACLFiltered} {
		values := h.Values(key)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] != "true" && values[0] != "false" {
			return m, ErrProtocol
		}
		v := values[0] == "true"
		*target = &v
	}
	for key, target := range map[string]*string{"X-Consul-Query-Backend": &m.QueryBackend, "X-Consul-Default-Acl-Policy": &m.DefaultACLPolicy} {
		values := h.Values(key)
		if len(values) > 1 {
			return m, ErrProtocol
		}
		if len(values) == 1 {
			*target = values[0]
		}
	}
	return m, nil
}

type Entry struct {
	PresentFields                              []string
	Key                                        string
	Value                                      []byte
	ValuePresent, ValueNull                    bool
	Flags, CreateIndex, ModifyIndex, LockIndex uint64
	Session                                    string
	SessionNull                                bool
}

func (e Entry) Clone() Entry {
	e.Value = bytes.Clone(e.Value)
	e.PresentFields = slices.Clone(e.PresentFields)
	return e
}
func (e Entry) HasField(name string) bool {
	_, found := slices.BinarySearch(e.PresentFields, name)
	return found
}

func decodeEntry(raw json.RawMessage) (Entry, error) {
	var fields map[string]json.RawMessage
	var e Entry
	if json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return e, ErrProtocol
	}
	for key := range fields {
		e.PresentFields = append(e.PresentFields, key)
	}
	slices.Sort(e.PresentFields)
	if json.Unmarshal(fields["Key"], &e.Key) != nil || !validKey(e.Key, false) {
		return e, ErrProtocol
	}
	for key, target := range map[string]*uint64{"Flags": &e.Flags, "CreateIndex": &e.CreateIndex, "ModifyIndex": &e.ModifyIndex, "LockIndex": &e.LockIndex} {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if string(value) == "null" || json.Unmarshal(value, target) != nil {
			return e, ErrProtocol
		}
	}
	if value, ok := fields["Value"]; ok {
		e.ValuePresent = true
		e.ValueNull = string(value) == "null"
		if !e.ValueNull {
			var encoded string
			if json.Unmarshal(value, &encoded) != nil {
				return e, ErrProtocol
			}
			b, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || base64.StdEncoding.EncodeToString(b) != encoded || len(b) > valueLimit {
				return e, ErrProtocol
			}
			e.Value = b
		}
	}
	if v, ok := fields["Session"]; ok {
		e.SessionNull = string(v) == "null"
		if !e.SessionNull {
			if json.Unmarshal(v, &e.Session) != nil || e.Session != "" && !validID(e.Session) {
				return e, ErrProtocol
			}
		}
	}
	return e, nil
}
func decodeEntries(raw []byte) ([]Entry, error) {
	var nodes []json.RawMessage
	if jsondocument.Decode(raw, &nodes, responseLimit) != nil || nodes == nil || len(nodes) > entryLimit {
		return nil, ErrProtocol
	}
	entries := make([]Entry, 0, len(nodes))
	seen := map[string]bool{}
	for _, node := range nodes {
		e, err := decodeEntry(node)
		if err != nil || seen[e.Key] {
			return nil, ErrProtocol
		}
		seen[e.Key] = true
		entries = append(entries, e)
	}
	return entries, nil
}

type TxnError struct {
	OpIndex int    `json:"OpIndex"`
	What    string `json:"What"`
}
type TxnResult struct {
	Committed bool
	Results   []Entry
	Errors    []TxnError
}

func (t TxnResult) Clone() TxnResult {
	t.Results = append([]Entry(nil), t.Results...)
	for i := range t.Results {
		t.Results[i] = t.Results[i].Clone()
	}
	t.Errors = append([]TxnError(nil), t.Errors...)
	return t
}
func decodeTxn(raw []byte, status, operations int) (TxnResult, error) {
	var fields map[string]json.RawMessage
	t := TxnResult{}
	if jsondocument.Decode(raw, &fields, responseLimit) != nil || fields["Results"] == nil || fields["Errors"] == nil {
		return t, ErrProtocol
	}
	var errorsRaw []map[string]json.RawMessage
	if json.Unmarshal(fields["Errors"], &errorsRaw) != nil || len(errorsRaw) > 64 {
		return t, ErrProtocol
	}
	seenErrors := map[int]bool{}
	for _, node := range errorsRaw {
		var e TxnError
		if len(node) != 2 || node["OpIndex"] == nil || node["What"] == nil ||
			bytes.Equal(bytes.TrimSpace(node["OpIndex"]), []byte("null")) ||
			json.Unmarshal(node["OpIndex"], &e.OpIndex) != nil || json.Unmarshal(node["What"], &e.What) != nil ||
			e.OpIndex < 0 || e.OpIndex >= operations || e.What == "" || seenErrors[e.OpIndex] {
			return t, ErrProtocol
		}
		seenErrors[e.OpIndex] = true
		t.Errors = append(t.Errors, e)
	}
	var nodes []map[string]json.RawMessage
	if json.Unmarshal(fields["Results"], &nodes) != nil || len(nodes) > entryLimit {
		return t, ErrProtocol
	}
	for _, node := range nodes {
		if len(node) != 1 || node["KV"] == nil {
			return t, ErrProtocol
		}
		e, err := decodeEntry(node["KV"])
		if err != nil {
			return t, err
		}
		t.Results = append(t.Results, e)
	}
	t.Committed = status == 200
	if t.Committed && len(t.Errors) > 0 || !t.Committed && (status != 409 || len(t.Errors) == 0 || len(t.Results) > 0) {
		return t, ErrProtocol
	}
	return t, nil
}
func projectResponse(r *Response, p Plan) error {
	if agentServiceWriteKind(p.kind) {
		return projectAgentServiceResponse(r, p.kind)
	}
	if r.Status >= 400 && r.Status < 500 && r.Status != http.StatusRequestTimeout && !(r.Status == 404 && p.kind == "kv-read") && !(r.Status == 409 && p.kind == "txn") {
		// Exact supported native endpoints reject these requests; no success is claimed.
		r.Acknowledged = true
		r.Succeeded = false
		return &HTTPError{r.Status}
	}
	if r.Status != 200 && !(r.Status == 404 && p.kind == "kv-read") && !(r.Status == 409 && p.kind == "txn") {
		return &HTTPError{r.Status}
	}
	var err error
	switch p.kind {
	case "kv-put", "kv-delete", "session-destroy":
		trimmed := bytes.TrimSpace(r.Body)
		if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return ErrProtocol
		}
		r.Acknowledged = true
		r.Succeeded = bytes.Equal(trimmed, []byte("true"))
		if !r.Succeeded {
			return ErrConflict
		}
	case "txn":
		t, e := decodeTxn(r.Body, r.Status, p.operations)
		if e != nil {
			return e
		}
		r.Txn = &t
		r.Acknowledged = true
		r.Succeeded = t.Committed
		if !t.Committed {
			return ErrConflict
		}
	case "kv-read":
		if r.Status == 404 {
			r.Acknowledged = true
			r.Succeeded = true
			break
		}
		if p.request.Query.Has("raw") {
			if len(r.Body) > valueLimit {
				return ErrBounds
			}
		}
		if p.request.Query.Has("keys") {
			var keys []string
			if jsondocument.Decode(r.Body, &keys, responseLimit) != nil || keys == nil || len(keys) > entryLimit {
				return ErrProtocol
			}
			prefix, _ := keyAt(p.request.Path, "/v1/kv/", true)
			seen := map[string]bool{}
			for _, key := range keys {
				if !validKey(key, false) || !strings.HasPrefix(key, prefix) || seen[key] {
					return ErrProtocol
				}
				seen[key] = true
			}
		} else if !p.request.Query.Has("raw") {
			r.Entries, err = decodeEntries(r.Body)
			if err != nil {
				return err
			}
			key, _ := keyAt(p.request.Path, "/v1/kv/", true)
			for _, entry := range r.Entries {
				if p.request.Query.Has("recurse") {
					if !strings.HasPrefix(entry.Key, key) {
						return ErrProtocol
					}
				} else if entry.Key != key {
					return ErrProtocol
				}
			}
			if !p.request.Query.Has("recurse") && len(r.Entries) != 1 {
				return ErrProtocol
			}
		}
		r.Acknowledged, r.Succeeded = true, true
	case "session-create":
		var v struct {
			ID string `json:"ID"`
		}
		if jsondocument.Decode(r.Body, &v, responseLimit) != nil || !validID(v.ID) {
			return ErrProtocol
		}
		r.SessionID = v.ID
		r.Acknowledged, r.Succeeded = true, true
	case "session-renew":
		var sessions []map[string]json.RawMessage
		if jsondocument.Decode(r.Body, &sessions, responseLimit) != nil || len(sessions) != 1 {
			return ErrProtocol
		}
		var id string
		if json.Unmarshal(sessions[0]["ID"], &id) != nil || id != strings.TrimPrefix(p.request.Path, "/v1/session/renew/") {
			return ErrProtocol
		}
		r.SessionID = id
		r.Acknowledged, r.Succeeded = true, true
	default:
		var v any
		if jsondocument.Decode(r.Body, &v, responseLimit) != nil {
			return ErrProtocol
		}
		r.Acknowledged, r.Succeeded = true, true
	}
	return nil
}

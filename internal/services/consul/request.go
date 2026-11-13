package consul

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const requestLimit = 8 << 20
const responseLimit = 48 << 20
const valueLimit = 512 << 10
const entryLimit = 10000

// Query never carries credentials or changes this original client's scope.
// Body and Query are copied before preparation; inspection copies grant no authority.
type Request struct {
	Method, Path string
	Query        url.Values
	Body         []byte
}

func (r Request) Clone() Request {
	r.Body = bytes.Clone(r.Body)
	if r.Query != nil {
		q := url.Values{}
		for k, v := range r.Query {
			q[k] = append([]string(nil), v...)
		}
		r.Query = q
	}
	return r
}

type Plan struct {
	owner      *Client
	request    Request
	kind       string
	write      bool
	used       *atomic.Bool
	operations int
}

func (p Plan) Snapshot() Request { return p.request.Clone() }
func (p Plan) Writes() bool      { return p.write }
func (c *Client) Prepare(r Request) (Plan, error) {
	kind, write, ops, e := validateRequest(r)
	if e != nil {
		return Plan{}, e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.retired {
		return Plan{}, ErrRetired
	}
	if write && c.unknown != nil {
		return Plan{}, ErrWriteUnknown
	}
	return Plan{owner: c, request: r.Clone(), kind: kind, write: write, used: &atomic.Bool{}, operations: ops}, nil
}
func validKey(s string, empty bool) bool {
	return (empty || len(s) > 0) && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func KeyPath(key string) (string, error) {
	if !validKey(key, true) {
		return "", ErrInvalid
	}
	if !canonicalHTTPKey(key) {
		return "", ErrUnsupported
	}
	return "/v1/kv/" + url.PathEscape(key), nil
}
func keyAt(path string, prefix string, empty bool) (string, error) {
	if !strings.HasPrefix(path, prefix) {
		return "", ErrUnsupported
	}
	tail := strings.TrimPrefix(path, prefix)
	key, e := url.PathUnescape(tail)
	if e != nil || !validKey(key, empty) || tail != url.PathEscape(key) {
		return "", ErrInvalid
	}
	return key, nil
}
func exactUint(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && strconv.FormatUint(n, 10) == s
}
func validID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validateRequest(r Request) (string, bool, int, error) {
	if len(r.Body) > requestLimit || len(r.Path) > 16384 || r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		return "", false, 0, ErrInvalid
	}
	if !strings.HasPrefix(r.Path, "/v1/") || strings.ContainsAny(r.Path, "?#\r\n\\ ") {
		return "", false, 0, ErrInvalid
	}
	for key, values := range r.Query {
		if len(values) != 1 || !validText(key, 128) || !validText(values[0], 4096) {
			return "", false, 0, ErrInvalid
		}
	}
	kind, write, ops := "", r.Method != http.MethodGet, 0
	allowed := map[string]bool{}
	readQuery := func() {
		for _, k := range []string{"index", "wait", "consistent", "stale"} {
			allowed[k] = true
		}
	}
	switch {
	case r.Path == "/v1/agent/service/register" && r.Method == http.MethodPut:
		if err := validateAgentServiceDefinition(r.Body); err != nil {
			return "", false, 0, err
		}
		kind = "agent-service-register"
		allowed["replace-existing-checks"] = true
	case strings.HasPrefix(r.Path, "/v1/agent/service/deregister/"):
		id, err := keyAt(r.Path, "/v1/agent/service/deregister/", false)
		if err != nil || !validText(id, 4096) || r.Method != http.MethodPut || len(r.Body) != 0 {
			return "", false, 0, ErrInvalid
		}
		if !canonicalHTTPKey(id) {
			return "", false, 0, ErrUnsupported
		}
		kind = "agent-service-deregister"
	case r.Path == "/v1/agent/check/register" && r.Method == http.MethodPut:
		if err := validateAgentDefinition(r.Body, validateAgentCheckRouting); err != nil {
			return "", false, 0, err
		}
		kind = "agent-check-register"
	case strings.HasPrefix(r.Path, "/v1/agent/check/deregister/"):
		id, err := keyAt(r.Path, "/v1/agent/check/deregister/", false)
		if err != nil || !validText(id, 4096) || r.Method != http.MethodPut || len(r.Body) != 0 {
			return "", false, 0, ErrInvalid
		}
		if !canonicalHTTPKey(id) {
			return "", false, 0, ErrUnsupported
		}
		kind = "agent-check-deregister"
	case strings.HasPrefix(r.Path, "/v1/kv/"):
		key, e := keyAt(r.Path, "/v1/kv/", true)
		if e == nil && !canonicalHTTPKey(key) {
			e = ErrUnsupported
		}
		if e != nil {
			return "", false, 0, e
		}
		if r.Method == http.MethodGet {
			if len(r.Body) > 0 {
				return "", false, 0, ErrInvalid
			}
			kind = "kv-read"
			readQuery()
			for _, k := range []string{"recurse", "keys", "separator", "raw"} {
				allowed[k] = true
			}
			if key == "" && !r.Query.Has("keys") && !r.Query.Has("recurse") || r.Query.Has("raw") && (r.Query.Has("keys") || r.Query.Has("recurse")) || r.Query.Has("keys") && r.Query.Has("recurse") || r.Query.Has("separator") && !r.Query.Has("keys") {
				return "", false, 0, ErrInvalid
			}
		} else if r.Method == http.MethodPut {
			kind = "kv-put"
			for _, k := range []string{"cas", "flags", "acquire", "release"} {
				allowed[k] = true
			}
			if key == "" || len(r.Body) > valueLimit || r.Query.Has("acquire") && r.Query.Has("release") || r.Query.Has("cas") && (r.Query.Has("acquire") || r.Query.Has("release")) {
				return "", false, 0, ErrInvalid
			}
		} else {
			kind = "kv-delete"
			allowed["cas"], allowed["recurse"] = true, true
			if len(r.Body) > 0 || key == "" && !r.Query.Has("recurse") || r.Query.Has("cas") && r.Query.Has("recurse") {
				return "", false, 0, ErrInvalid
			}
		}
	case r.Path == "/v1/txn" && r.Method == http.MethodPut:
		var e error
		ops, write, e = validateTxn(r.Body)
		if e != nil {
			return "", false, 0, e
		}
		kind = "txn"
		if !write {
			readQuery()
		}
	case r.Path == "/v1/session/create" && r.Method == http.MethodPut:
		kind = "session-create"
		if e := validateSessionCreate(r.Body); e != nil {
			return "", false, 0, e
		}
	case strings.HasPrefix(r.Path, "/v1/session/renew/") || strings.HasPrefix(r.Path, "/v1/session/destroy/"):
		prefix := "/v1/session/renew/"
		kind = "session-renew"
		if strings.HasPrefix(r.Path, "/v1/session/destroy/") {
			prefix = "/v1/session/destroy/"
			kind = "session-destroy"
		}
		id, e := keyAt(r.Path, prefix, false)
		if e != nil || !validID(id) || r.Method != http.MethodPut || len(r.Body) > 0 {
			return "", false, 0, ErrInvalid
		}
	case r.Method == http.MethodGet:
		if len(r.Body) > 0 {
			return "", false, 0, ErrInvalid
		}
		kind = "read"
		switch r.Path {
		case "/v1/agent/self", "/v1/status/leader", "/v1/status/peers", "/v1/catalog/datacenters":
		case "/v1/agent/services", "/v1/agent/checks":
			allowed["filter"] = true
		case "/v1/catalog/nodes", "/v1/catalog/services", "/v1/session/list":
			readQuery()
		default:
			match := false
			for _, prefix := range []string{"/v1/catalog/service/", "/v1/catalog/node/", "/v1/health/state/", "/v1/health/node/", "/v1/health/checks/", "/v1/health/service/", "/v1/session/info/", "/v1/session/node/"} {
				if strings.HasPrefix(r.Path, prefix) {
					key, e := keyAt(r.Path, prefix, false)
					if e != nil {
						return "", false, 0, e
					}
					if prefix == "/v1/session/info/" && !validID(key) {
						return "", false, 0, ErrInvalid
					}
					match = true
					readQuery()
					if strings.HasPrefix(prefix, "/v1/health/") {
						allowed["filter"], allowed["near"] = true, true
					}
					if prefix == "/v1/health/service/" {
						allowed["passing"] = true
					}
					break
				}
			}
			if !match {
				return "", false, 0, ErrUnsupported
			}
		}
	default:
		return "", false, 0, ErrUnsupported
	}
	for k, values := range r.Query {
		if !allowed[k] {
			return "", false, 0, ErrInvalid
		}
		v := values[0]
		switch k {
		case "index", "cas", "flags":
			if !exactUint(v) {
				return "", false, 0, ErrInvalid
			}
		case "filter", "near":
			// Native read options retain their authored string, including empty.
		case "passing", "replace-existing-checks":
			if v != "" && v != "true" && v != "false" {
				return "", false, 0, ErrInvalid
			}
		case "wait":
			d, e := time.ParseDuration(v)
			if e != nil || d <= 0 || d > 10*time.Minute || !r.Query.Has("index") {
				return "", false, 0, ErrInvalid
			}
		case "acquire", "release":
			if !validID(v) {
				return "", false, 0, ErrInvalid
			}
		case "separator":
			if len(v) > 16 {
				return "", false, 0, ErrInvalid
			}
		default:
			if v != "" {
				return "", false, 0, ErrInvalid
			}
		}
	}
	if r.Query.Has("consistent") && r.Query.Has("stale") {
		return "", false, 0, ErrInvalid
	}
	return kind, write, ops, nil
}

type TxnKV struct {
	Verb    string  `json:"Verb"`
	Key     string  `json:"Key"`
	Value   *[]byte `json:"Value,omitempty"`
	Flags   *uint64 `json:"Flags,omitempty"`
	Index   *uint64 `json:"Index,omitempty"`
	Session *string `json:"Session,omitempty"`
}
type TxnOperation struct {
	KV *TxnKV `json:"KV"`
}

func TxnRequest(ops []TxnOperation) (Request, error) {
	raw, e := json.Marshal(ops)
	if e != nil {
		return Request{}, e
	}
	r := Request{Method: http.MethodPut, Path: "/v1/txn", Body: raw}
	_, _, _, e = validateRequest(r)
	return r, e
}
func validateTxn(raw []byte) (int, bool, error) {
	var nodes []map[string]json.RawMessage
	if jsondocument.Decode(raw, &nodes, requestLimit) != nil || len(nodes) == 0 || len(nodes) > 64 {
		return 0, false, ErrInvalid
	}
	writes := false
	for _, node := range nodes {
		if len(node) != 1 || node["KV"] == nil {
			return 0, false, ErrUnsupported
		}
		var fields map[string]json.RawMessage
		var op TxnKV
		if json.Unmarshal(node["KV"], &fields) != nil || json.Unmarshal(node["KV"], &op) != nil || !validKey(op.Key, false) || op.Verb == "" {
			return 0, false, ErrInvalid
		}
		for k, v := range fields {
			if string(v) == "null" || k != "Verb" && k != "Key" && k != "Value" && k != "Flags" && k != "Index" && k != "Session" {
				return 0, false, ErrInvalid
			}
		}
		allowValue, allowFlags, allowIndex, allowSession := false, false, false, false
		switch op.Verb {
		case "get", "get-tree", "check-not-exists":
		case "check-index":
			allowIndex = true
		case "check-session":
			allowSession = true
		case "set":
			writes = true
			allowValue, allowFlags = true, true
		case "cas":
			writes = true
			allowValue, allowFlags, allowIndex = true, true, true
		case "delete", "delete-tree":
			writes = true
		case "delete-cas":
			writes = true
			allowIndex = true
		case "lock", "unlock":
			writes = true
			allowValue, allowFlags, allowSession = true, true, true
		default:
			return 0, false, ErrUnsupported
		}
		if op.Value != nil && (!allowValue || len(*op.Value) > valueLimit) || op.Flags != nil && !allowFlags || allowIndex != (op.Index != nil) || allowSession != (op.Session != nil) || op.Session != nil && !validID(*op.Session) || allowValue && op.Value == nil {
			return 0, false, ErrInvalid
		}
	}
	return len(nodes), writes, nil
}
func validateSessionCreate(raw []byte) error {
	var fields map[string]json.RawMessage
	if jsondocument.Decode(raw, &fields, requestLimit) != nil || fields == nil {
		return ErrInvalid
	}
	for key, v := range fields {
		switch key {
		case "Name", "Node", "Behavior", "TTL":
			var s string
			if string(v) == "null" || json.Unmarshal(v, &s) != nil || !validText(s, 4096) {
				return ErrInvalid
			}
			if key == "Behavior" && s != "delete" && s != "release" {
				return ErrInvalid
			}
			if key == "TTL" {
				d, e := time.ParseDuration(s)
				if e != nil || d < 10*time.Second || d > 24*time.Hour {
					return ErrInvalid
				}
			}
		case "LockDelay":
			var s string
			if json.Unmarshal(v, &s) != nil {
				return ErrInvalid
			}
			d, e := time.ParseDuration(s)
			if e != nil || d < 0 || d > 60*time.Second {
				return ErrInvalid
			}
		case "NodeChecks":
			var list []string
			if string(v) == "null" || json.Unmarshal(v, &list) != nil || len(list) > 64 {
				return ErrInvalid
			}
			for _, s := range list {
				if !validText(s, 4096) || s == "" {
					return ErrInvalid
				}
			}
		default:
			return ErrUnsupported
		}
	}
	return nil
}

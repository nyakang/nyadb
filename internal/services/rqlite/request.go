package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nyakang/nyadb/internal/db/contract"
)

const maxReceiptBytes = 32 << 20
const maxRequestBytes = 8 << 20

var errIncomplete = errors.New("RQLite original response is incomplete or invalid")
var errResponseLost = errors.New("RQLite original request lost its response")

// receipt is private and retains the full original body independently of every
// typed projection. Transport loss never becomes a synthetic rollback receipt.
type receipt struct {
	Results   []nativeResult `json:"results"`
	Error     string         `json:"error"`
	Raw       string         `json:"-"`
	Uncertain bool           `json:"-"`
}
type nativeResult struct {
	Columns  []string            `json:"columns"`
	Types    []string            `json:"types"`
	Values   [][]json.RawMessage `json:"values"`
	Affected json.Number         `json:"rows_affected"`
	Error    string              `json:"error"`
	ErrorV2  json.RawMessage     `json:"error_v2"`
}

// request registers under the session lock before dispatch. Caller cancellation
// can stop admission, but after dispatch we consume the original bounded HTTP
// result. Losing that result leaves possible writes uncertain and unreplayed.
func (s *Session) request(ctx context.Context, path string, statements []any, transaction, possibleWrite bool) (out receipt, err error) {
	if len(statements) == 0 || len(statements) > 1024 {
		return out, errIncomplete
	}
	body, e := json.Marshal(statements)
	if e != nil || len(body) > maxRequestBytes {
		return out, errors.New("RQLite request exceeds its supported bounds")
	}
	select {
	case <-ctx.Done():
		return out, ctx.Err()
	case <-s.closing:
		return out, contract.ErrRevoked
	case s.requests <- struct{}{}:
	}
	defer func() { <-s.requests }()
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, contract.ErrRevoked
	}
	password, version := s.password, s.version
	s.active.Add(1)
	s.mu.Unlock()
	defer s.active.Done()
	o := s.config.Options
	u, e := url.Parse(s.config.Endpoint)
	if e != nil {
		return out, errors.New("RQLite endpoint is invalid")
	}
	u.Path += path
	params := url.Values{"level": {o.Consistency}, "timeout": {strconv.Itoa(o.ForwardTimeoutSeconds) + "s"}, "db_timeout": {strconv.Itoa(o.DatabaseTimeoutMilliseconds) + "ms"}, "linearizable_timeout": {strconv.Itoa(o.LinearizableTimeoutMilliseconds) + "ms"}, "retries": {"0"}, "blob_array": {""}, "timings": {""}}
	if transaction {
		params.Set("transaction", "")
	}
	if o.FreshnessMilliseconds > 0 {
		params.Set("freshness", strconv.Itoa(o.FreshnessMilliseconds)+"ms")
	}
	if o.FreshnessStrict {
		params.Set("freshness_strict", "")
	}
	u.RawQuery = params.Encode()
	child, cancel := context.WithTimeout(context.Background(), time.Duration(o.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(child, http.MethodPost, u.String(), bytes.NewReader(body))
	if e != nil {
		return out, errors.New("RQLite endpoint is invalid")
	}
	// No GetBody: neither this client nor its transport may replay POST data.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if o.Auth == "basic" {
		req.SetBasicAuth(s.config.Username, password)
	}
	res, e := s.client.Do(req)
	if e != nil {
		out.Uncertain = possibleWrite
		return out, errResponseLost
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, maxReceiptBytes+1))
	if e != nil || len(raw) > maxReceiptBytes {
		out.Uncertain = possibleWrite
		return out, errIncomplete
	}
	if res.StatusCode == http.StatusOK && utf8.Valid(raw) && json.Valid(raw) {
		out.Raw = string(raw)
	} else {
		// Even a malformed or non-JSON response is original evidence. Keep its
		// complete bytes and status in a JSON envelope, never a partial body.
		encoded, _ := json.Marshal(struct {
			Status      int    `json:"http_status"`
			ContentType string `json:"content_type"`
			Body        []byte `json:"body_base64"`
		}{res.StatusCode, res.Header.Get("Content-Type"), raw})
		out.Raw = string(encoded)
	}
	if res.StatusCode != http.StatusOK {
		// Authentication/parser rejection cannot execute; redirect is never
		// followed. Server/forwarding failures can conceal an applied write.
		out.Uncertain = possibleWrite && res.StatusCode != 400 && res.StatusCode != 401 && res.StatusCode != 403 && res.StatusCode != 404 && res.StatusCode != 405 && res.StatusCode != 413 && res.StatusCode != 415
		return out, fmt.Errorf("RQLite HTTP request failed (status %d)", res.StatusCode)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !utf8.Valid(raw) || !json.Valid(raw) || d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF {
		out.Uncertain = possibleWrite
		return out, errIncomplete
	}
	actualVersion := res.Header.Get("X-RQLITE-VERSION")
	if !serverVersion.MatchString(actualVersion) || version != "" && actualVersion != version {
		out.Uncertain = possibleWrite
		return out, errors.New("RQLite original product identity changed or is missing")
	}
	s.mu.Lock()
	if s.version == "" {
		s.version = actualVersion
	}
	s.mu.Unlock()
	if out.Error != "" || len(out.Results) == 0 || len(out.Results) > len(statements) {
		out.Uncertain = possibleWrite
		return out, errIncomplete
	}
	failed := false
	for _, result := range out.Results {
		if result.failed() {
			failed = true
		}
	}
	if len(out.Results) < len(statements) && !(transaction && failed) {
		out.Uncertain = possibleWrite
		return out, errIncomplete
	}
	for _, result := range out.Results {
		if result.failed() {
			return out, result.safeError()
		}
	}
	return out, nil // A complete native receipt wins a caller-cancel race.
}

func (r nativeResult) failed() bool {
	return r.Error != "" || len(r.ErrorV2) > 0 && string(r.ErrorV2) != "null"
}
func (r nativeResult) safeError() error {
	var detail struct {
		Message string                           `json:"message"`
		SQLite  struct{ Code, ExtendedCode int } `json:"sqlite"`
	}
	if json.Unmarshal(r.ErrorV2, &detail) == nil && detail.SQLite.Code > 0 {
		// Native messages may echo SQL or secrets; full originals stay in Raw.
		return fmt.Errorf("RQLite SQLite statement failed (code %d)", detail.SQLite.Code)
	}
	if strings.EqualFold(r.Error, "query timeout") || strings.EqualFold(r.Error, "execute timeout") || strings.EqualFold(detail.Message, "query timeout") || strings.EqualFold(detail.Message, "execute timeout") {
		return context.DeadlineExceeded
	}
	return errors.New("RQLite statement failed; original receipt retained")
}

func (r nativeResult) cells(row []json.RawMessage) ([]contract.Cell, error) {
	if len(row) != len(r.Columns) || len(r.Types) != len(r.Columns) {
		return nil, errIncomplete
	}
	out := make([]contract.Cell, len(row))
	for i, raw := range row {
		if bytes.Equal(raw, []byte("null")) {
			out[i].Kind = contract.Null
			continue
		}
		var value any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&value) != nil {
			return nil, errIncomplete
		}
		switch v := value.(type) {
		case json.Number:
			kind := contract.Floating
			if !strings.ContainsAny(v.String(), ".eE") {
				if _, e := strconv.ParseInt(v.String(), 10, 64); e != nil {
					return nil, errIncomplete
				}
				kind = contract.Integer
				if r.Types[i] == "real" || r.Types[i] == "float" || r.Types[i] == "double" {
					kind = contract.Floating
				}
			}
			out[i] = contract.Cell{Kind: kind, Text: v.String()}
		case string:
			out[i] = contract.Cell{Kind: contract.Text, Text: v}
		case []any:
			b := make([]byte, len(v))
			for j, n := range v {
				number, ok := n.(json.Number)
				if !ok {
					return nil, errIncomplete
				}
				byteValue, e := strconv.ParseUint(number.String(), 10, 8)
				if e != nil {
					return nil, errIncomplete
				}
				b[j] = byte(byteValue)
			}
			out[i] = contract.Cell{Kind: contract.Binary, Bytes: b}
		default:
			return nil, errIncomplete
		}
	}
	return out, nil
}

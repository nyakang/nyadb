package consul

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const sessionProjectionLimit = 10000

func sessionProjectionTarget(r contract.KVHTTPRequest) (string, bool) {
	if r.Method != "GET" {
		return "", false
	}
	if r.Path == "/v1/session/list" {
		return "", true
	}
	for _, prefix := range []string{"/v1/session/info/", "/v1/session/node/"} {
		if !strings.HasPrefix(r.Path, prefix) {
			continue
		}
		value, err := url.PathUnescape(strings.TrimPrefix(r.Path, prefix))
		if err != nil || value == "" || url.PathEscape(value) != strings.TrimPrefix(r.Path, prefix) {
			return "", false
		}
		if prefix == "/v1/session/info/" {
			return value, validID(value)
		}
		return "", true
	}
	return "", false
}

// This optional projection runs after native HTTP classification. Parsing
// failure cannot turn an acknowledged response into an unknown write or make
// a later observation acknowledge an earlier write.
func projectDocumentSessions(out *contract.KVResult) {
	h := out.HTTP
	expected, ok := sessionProjectionTarget(h.Request)
	if !ok || h.Status != 200 || !h.Captured || !h.EOF || !h.Acknowledged || !h.Succeeded {
		return
	}
	list, err := decodeSessionProjection(h.Body, expected)
	if err != nil {
		message := "Session projection unavailable; original HTTP body and acknowledgement retained: " + err.Error()
		if h.ProjectionError != "" {
			message = h.ProjectionError + "; " + message
		}
		h.ProjectionError, out.ProjectionError = message, message
		return
	}
	h.Sessions = &list
}

// Validation uses the shared duplicate/Unicode/depth checks without asking
// encoding/json to allocate an unbounded generic object or RawMessage array.
type sessionProjectionJSON struct{}

func (*sessionProjectionJSON) UnmarshalJSON([]byte) error { return nil }

func sessionProjectionArray(raw []byte, limit int, consume func(json.RawMessage) error) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('[') {
		return ErrProtocol
	}
	count := 0
	for d.More() {
		if count >= limit {
			return ErrBounds
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return ErrProtocol
		}
		if len(value) > projectionBudget {
			return ErrBounds
		}
		if err := consume(value); err != nil {
			return err
		}
		count++
	}
	if token, err = d.Token(); err != nil || token != json.Delim(']') {
		return ErrProtocol
	}
	if _, err = d.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}
func sessionProjectionFields(raw json.RawMessage) (map[string]json.RawMessage, []string, []string, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, nil, nil, ErrProtocol
	}
	fields := map[string]json.RawMessage{}
	present, nulls := []string{}, []string(nil)
	for d.More() {
		if len(fields) >= 256 {
			return nil, nil, nil, ErrBounds
		}
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, nil, nil, ErrProtocol
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, nil, nil, ErrProtocol
		}
		fields[key] = value
		present = append(present, key)
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			nulls = append(nulls, key)
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, nil, nil, ErrProtocol
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, nil, nil, ErrProtocol
	}
	slices.Sort(present)
	slices.Sort(nulls)
	return fields, present, nulls, nil
}
func decodeSessionProjection(raw []byte, expected string) (contract.KVHTTPSessionList, error) {
	var valid sessionProjectionJSON
	if err := jsondocument.Decode(raw, &valid, responseLimit); err != nil {
		return contract.KVHTTPSessionList{}, errors.Join(ErrProtocol, err)
	}
	list := contract.KVHTTPSessionList{Rows: []contract.KVHTTPSession{}}
	seen := map[string]bool{}
	cost := int64(0)
	limit := sessionProjectionLimit
	if expected != "" {
		limit = 1
	}
	err := sessionProjectionArray(raw, limit, func(raw json.RawMessage) error {
		row, err := decodeSessionRow(raw)
		if err != nil {
			return err
		}
		if seen[row.ID] || expected != "" && row.ID != expected {
			return ErrProtocol
		}
		seen[row.ID] = true
		cost += sessionProjectionCost(row)
		if cost > projectionBudget {
			return ErrBounds
		}
		list.Rows = append(list.Rows, row)
		return nil
	})
	if err != nil {
		return contract.KVHTTPSessionList{}, err
	}
	return list, nil
}
func decodeSessionRow(raw json.RawMessage) (contract.KVHTTPSession, error) {
	var row contract.KVHTTPSession
	fields, present, nulls, err := sessionProjectionFields(raw)
	if err != nil {
		return row, err
	}
	row.PresentFields, row.NullFields = present, nulls
	if json.Unmarshal(fields["ID"], &row.ID) != nil || !validID(row.ID) {
		return row, ErrProtocol
	}
	for key, dst := range map[string]*string{"Name": &row.Name, "Node": &row.Node, "TTL": &row.TTL, "Behavior": &row.Behavior, "Namespace": &row.Namespace, "Partition": &row.Partition} {
		if value, exists := fields[key]; exists && !slices.Contains(nulls, key) {
			if json.Unmarshal(value, dst) != nil {
				return row, ErrProtocol
			}
		}
	}
	for key, dst := range map[string]*uint64{"LockDelay": &row.LockDelay, "CreateIndex": &row.CreateIndex, "ModifyIndex": &row.ModifyIndex} {
		if value, exists := fields[key]; exists && !slices.Contains(nulls, key) {
			if json.Unmarshal(value, dst) != nil {
				return row, ErrProtocol
			}
		}
	}

	for key, dst := range map[string]*[]string{"Checks": &row.Checks, "NodeChecks": &row.NodeChecks} {
		if value, exists := fields[key]; exists && !slices.Contains(nulls, key) {
			*dst = []string{}
			err := sessionProjectionArray(value, sessionProjectionLimit, func(value json.RawMessage) error {
				var check string
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &check) != nil {
					return ErrProtocol
				}
				*dst = append(*dst, check)
				return nil
			})
			if err != nil {
				return row, err
			}
		}
	}
	if value, exists := fields["ServiceChecks"]; exists && !slices.Contains(nulls, "ServiceChecks") {
		row.ServiceChecks = []contract.KVHTTPSessionServiceCheck{}
		err := sessionProjectionArray(value, sessionProjectionLimit, func(value json.RawMessage) error {
			fields, present, nulls, err := sessionProjectionFields(value)
			if err != nil {
				return err
			}
			check := contract.KVHTTPSessionServiceCheck{PresentFields: present, NullFields: nulls}
			if json.Unmarshal(fields["ID"], &check.ID) != nil || check.ID == "" {
				return ErrProtocol
			}
			if value, exists := fields["Namespace"]; exists && !slices.Contains(nulls, "Namespace") {
				if json.Unmarshal(value, &check.Namespace) != nil {
					return ErrProtocol
				}
			}
			row.ServiceChecks = append(row.ServiceChecks, check)
			return nil
		})
		if err != nil {
			return row, err
		}
	}

	return row, nil
}
func sessionProjectionCost(row contract.KVHTTPSession) int64 {
	cost := int64(256 + len(row.ID) + len(row.Name) + len(row.Node) + len(row.TTL) + len(row.Behavior) + len(row.Namespace) + len(row.Partition))
	for _, values := range [][]string{row.PresentFields, row.NullFields, row.Checks, row.NodeChecks} {
		for _, value := range values {
			cost += int64(24 + len(value))
		}
	}
	for _, check := range row.ServiceChecks {
		cost += int64(96 + len(check.ID) + len(check.Namespace))
		for _, values := range [][]string{check.PresentFields, check.NullFields} {
			for _, value := range values {
				cost += int64(24 + len(value))
			}
		}
	}
	return cost
}

// Package agentbridge owns a captured external driver process and its JSON-RPC
// sessions. It does not choose database products or publish workbench generations.
package agentbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	ErrLaunch        = errors.New("database agent could not be started")
	ErrArtifact      = errors.New("database agent artifact verification failed")
	ErrProtocol      = errors.New("database agent protocol is invalid")
	ErrLost          = errors.New("original database agent exited; reconnect explicitly")
	ErrClosed        = errors.New("original database agent session is closed")
	ErrRequest       = errors.New("database agent request is invalid")
	ErrLimit         = errors.New("database agent request exceeds its resource limit")
	ErrVersion       = errors.New("database agent does not support session protocol v2")
	ErrSingleVersion = errors.New("database agent does not support single-session protocol v1")
)

// Receipt retains the original complete result even when cancellation raced
// with success. Cancel acknowledgment alone is never executor acknowledgment.
// A process exit does not prove a remote write committed or rolled back.
type Receipt struct {
	Result                json.RawMessage
	RemoteError           *RemoteError
	Dispatched            bool
	ExecutorAcknowledged  bool
	CancellationRequested bool
	ProcessExitConfirmed  bool
	original              json.RawMessage
}

// OriginalResponse returns an owned copy of a single-session v1 JSON-RPC
// response (nil for v2 receipts). It may contain credentials or values in native
// error text. Only explicit native receipt inspection/export should access it; ordinary errors
// and JSON serialization of Receipt keep the sanitized RemoteError instead.
func (r Receipt) OriginalResponse() json.RawMessage {
	return append(json.RawMessage(nil), r.original...)
}

// RemoteError deliberately excludes native message text, which can contain
// credentials or SQL values. Retryable is a native hint, never replay authority.
type RemoteError struct {
	Code                                                            int64
	ContractVersion                                                 int
	Category, SessionDisposition, Stage, OperationOutcome, SQLState string
	VendorCode                                                      *int64
	Retryable                                                       bool
}

func (e *RemoteError) Error() string {
	if e == nil {
		return "database agent request failed"
	}
	s := "database agent request failed"
	if e.Category != "" {
		s += " (" + e.Category + ")"
	}
	if e.SQLState != "" {
		s += " SQLSTATE " + e.SQLState
	}
	if e.VendorCode != nil {
		s += " vendor " + strconv.FormatInt(*e.VendorCode, 10)
	}
	return s
}

type Handshake struct {
	ProtocolVersion      int      `json:"protocolVersion"`
	AgentProtocolVersion int      `json:"agentProtocolVersion"`
	Capabilities         []string `json:"capabilities"`
}

func (h Handshake) Supports(capability string) bool {
	for _, c := range h.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

type response struct {
	result   json.RawMessage
	original json.RawMessage
	remote   *RemoteError
	err      error
}

// objectFields rejects repeated keys and trailing JSON before routing IDs or
// injecting session ownership. Native values remain raw; numbers never round
// through float64, including integers larger than JavaScript's safe range.
func objectFields(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, ErrProtocol
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return nil, ErrProtocol
		}
		key, ok := t.(string)
		if !ok {
			return nil, ErrProtocol
		}
		if _, exists := fields[key]; exists {
			return nil, ErrProtocol
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrProtocol
		}
		fields[key] = value
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') {
		return nil, ErrProtocol
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrProtocol
	}
	return fields, nil
}

func snapshotParams(params any, sessionID string) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, ErrRequest
	}
	if params == nil {
		raw = []byte("{}")
	}
	fields, err := objectFields(raw)
	if err != nil {
		return nil, ErrRequest
	}
	if _, exists := fields["agentSessionId"]; exists {
		return nil, ErrRequest
	}
	if sessionID != "" {
		fields["agentSessionId"], _ = json.Marshal(sessionID)
	}
	return json.Marshal(fields)
}

func validToken(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func controlMethod(method string) bool {
	switch method {
	case "handshake", "connect", "disconnect", "open_session", "close_session", "cancel_session", "test_connection", "shutdown":
		return true
	}
	return false
}

func decodeRemote(raw json.RawMessage, structured bool, sessionID string) (*RemoteError, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return nil, err
	}
	e := &RemoteError{}
	code, ok := fields["code"]
	if !ok || json.Unmarshal(code, &e.Code) != nil {
		return nil, ErrProtocol
	}
	if message, ok := fields["message"]; !ok || !json.Valid(message) {
		return nil, ErrProtocol
	}
	if !structured {
		return e, nil
	}
	data, ok := fields["data"]
	if !ok {
		return nil, ErrProtocol
	}
	d, err := objectFields(data)
	if err != nil {
		return nil, err
	}
	var details struct {
		ContractVersion    int    `json:"contractVersion"`
		Category           string `json:"category"`
		Retryable          bool   `json:"retryable"`
		SessionDisposition string `json:"sessionDisposition"`
		Stage              string `json:"stage"`
		OperationOutcome   string `json:"operationOutcome"`
		SQLState           string `json:"sqlState"`
		VendorCode         *int64 `json:"vendorCode"`
		SessionID          string `json:"agentSessionId"`
	}
	if json.Unmarshal(data, &details) != nil || details.ContractVersion != 1 {
		return nil, ErrProtocol
	}
	for _, key := range []string{"category", "retryable", "sessionDisposition", "stage", "operationOutcome"} {
		if _, ok := d[key]; !ok {
			return nil, ErrProtocol
		}
	}
	if details.SessionID != "" && details.SessionID != sessionID {
		return nil, ErrProtocol
	}
	if !validToken(details.Category) || !validToken(details.Stage) {
		return nil, ErrProtocol
	}
	switch details.SessionDisposition {
	case "keep", "quarantine", "replace_runtime":
	default:
		return nil, ErrProtocol
	}
	switch details.OperationOutcome {
	case "not_started", "unknown", "completed":
	default:
		return nil, ErrProtocol
	}
	if details.SQLState != "" {
		if len(details.SQLState) != 5 {
			return nil, ErrProtocol
		}
		for _, c := range details.SQLState {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
				return nil, ErrProtocol
			}
		}
	}
	e.ContractVersion, e.Category, e.Retryable = details.ContractVersion, details.Category, details.Retryable
	e.SessionDisposition, e.Stage, e.OperationOutcome = details.SessionDisposition, details.Stage, details.OperationOutcome
	e.SQLState, e.VendorCode = details.SQLState, details.VendorCode
	return e, nil
}

func decodeHandshake(raw json.RawMessage) (Handshake, error) {
	return decodeHandshakeVersion(raw, 2)
}

func decodeHandshakeVersion(raw json.RawMessage, version int) (Handshake, error) {
	var h Handshake
	if _, err := objectFields(raw); err != nil {
		return h, err
	}
	if json.Unmarshal(raw, &h) != nil || h.ProtocolVersion != version || h.AgentProtocolVersion != version || !h.Supports("connect") || h.Supports("multi_session") != (version == 2) {
		if version == 1 {
			return Handshake{}, ErrSingleVersion
		}
		return Handshake{}, ErrVersion
	}
	if len(h.Capabilities) > 256 {
		return Handshake{}, ErrProtocol
	}
	seen := map[string]bool{}
	for _, c := range h.Capabilities {
		if !validToken(c) || seen[c] {
			return Handshake{}, ErrProtocol
		}
		seen[c] = true
	}
	return h, nil
}

func errorWithCancellation(err, canceled error) error {
	if canceled == nil {
		return err
	}
	if err == nil {
		return canceled
	}
	return errors.Join(canceled, err)
}

func rawString(raw json.RawMessage) (string, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", ErrProtocol
	}
	return value, nil
}

func normalizedType(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func protocolField(name string) error { return fmt.Errorf("%w: %s", ErrProtocol, name) }

// Package jdbcplugin owns the DBX external JDBC plugin's default connection and
// captured process. Its line protocol is distinct from the database agents.
package jdbcplugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

var (
	ErrLaunch   = errors.New("external JDBC process could not be started")
	ErrArtifact = errors.New("external JDBC artifact verification failed")
	ErrProtocol = errors.New("external JDBC response is invalid")
	ErrLost     = errors.New("original external JDBC process exited; reconnect explicitly")
	ErrClosed   = errors.New("original external JDBC connection is closed")
	ErrRequest  = errors.New("external JDBC request is invalid")
	ErrLimit    = errors.New("external JDBC request exceeds its resource limit")
	ErrRemote   = errors.New("original external JDBC executor reported an error")
)

// Receipt retains the original executor's response independently of later
// observations. Neither cancellation nor process exit proves remote rollback.
// Root native error text can contain secrets; ordinary serialization excludes
// it. Execute validates typed diagnostics; Call does not admit callers supplying
// nativeDiagnostics. Query data is retained without redaction.
type Receipt struct {
	Diagnostic                                                                    *Diagnostic
	Result                                                                        json.RawMessage
	RemoteFailure                                                                 bool
	Dispatched, ExecutorAcknowledged, CancellationRequested, ProcessExitConfirmed bool
	original, cancellation                                                        json.RawMessage
}

func (r Receipt) OriginalResponse() json.RawMessage {
	return append(json.RawMessage(nil), r.original...)
}
func (r Receipt) CancellationResponse() json.RawMessage {
	return append(json.RawMessage(nil), r.cancellation...)
}
func (r Receipt) Clone() Receipt {
	if r.Diagnostic != nil {
		d := *r.Diagnostic
		r.Diagnostic = &d
	}
	r.Result = append(json.RawMessage(nil), r.Result...)
	r.original = append(json.RawMessage(nil), r.original...)
	r.cancellation = append(json.RawMessage(nil), r.cancellation...)
	return r
}

type response struct {
	receipt Receipt
	err     error
}

// Retain number lexemes and reject duplicate routing keys and trailing data.
func object(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrProtocol
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, ErrProtocol
		}
		key, ok := token.(string)
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
	if token, err = d.Token(); err != nil || token != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return nil, ErrProtocol
	}
	return fields, nil
}
func okResult(raw json.RawMessage) bool {
	fields, err := object(raw)
	var ok bool
	return err == nil && json.Unmarshal(fields["ok"], &ok) == nil && ok
}

// StartupReceipt separates negotiation, original connection acknowledgement and
// confirmed process disposal. Failed connections retain their complete original
// response through Connect.OriginalResponse without logging native error text.
type StartupReceipt struct {
	Protocol, Connect    Receipt
	ProcessExitConfirmed bool
}

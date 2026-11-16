package agentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestV2OriginalResponsesRequireOptInAndRemainPrivate(t *testing.T) {
	for _, retain := range []bool{false, true} {
		c := helperClient(t, "normal", Options{RetainOriginalResponses: retain})
		s := helperSession(t, c)
		r, err := s.Call(context.Background(), "native_error", map[string]string{"secret": "private original error text"})
		if err == nil || !r.ExecutorAcknowledged {
			t.Fatal("original executor error missing", err)
		}
		raw := r.OriginalResponse()
		if retain != bytes.Contains(raw, []byte("private original error text")) {
			t.Fatal("original native envelope retention policy changed")
		}
		ordinary, err := json.Marshal(r)
		if err != nil || bytes.Contains(ordinary, []byte("private original error text")) {
			t.Fatal("explicit native error bytes leaked into ordinary receipt")
		}
		if len(raw) > 0 {
			raw[0] = '!'
			if !json.Valid(r.OriginalResponse()) {
				t.Fatal("original response aliased its accessor")
			}
		}
		ready, handshake := c.ReadyResponse(), c.HandshakeReceipt()
		if retain {
			if !json.Valid(ready) || !json.Valid(handshake.OriginalResponse()) {
				t.Fatal("original readiness/handshake evidence missing")
			}
			ready[0] = '!'
			if !json.Valid(c.ReadyResponse()) {
				t.Fatal("original readiness accessor aliased retained bytes")
			}
		} else if len(ready) != 0 || len(handshake.OriginalResponse()) != 0 {
			t.Fatal("ordinary v2 runtime retained private original envelopes")
		}
	}
}

func TestNativeCQLCodesRemainSeparateFromStrictSQLState(t *testing.T) {
	for _, codes := range []ErrorCodeFormat{SQLStateCodes, CQLCodes} {
		for _, value := range []string{"0x2000", "0x2200", "0x20zz", "42000", "0x22000", "secret"} {
			raw, _ := json.Marshal(map[string]any{"code": -1, "message": "private native error", "data": map[string]any{
				"contractVersion": 1, "category": "sql", "stage": "execute", "sessionDisposition": "keep",
				"operationOutcome": "unknown", "retryable": false, "sqlState": value}})
			remote, err := decodeRemote(raw, true, "", codes)
			valid := codes == SQLStateCodes && value == "42000" || codes == CQLCodes && (value == "0x2000" || value == "0x2200")
			if (err == nil) != valid {
				t.Fatal("original code format accepted an incompatible/error-text representation", codes, value, err)
			}
			if valid && codes == CQLCodes && (remote.NativeCode != value || remote.SQLState != "" || bytes.Contains([]byte(remote.Error()), []byte("private native error"))) {
				t.Fatal("original CQL code became SQLSTATE or leaked error text")
			}
		}
	}
	if _, err := normalizeOptions(Options{ErrorCodes: DatabendCodes + 1}); err == nil {
		t.Fatal("undefined native error code representation accepted")
	}
}

func TestDatabendOriginalQueryIDPrefixDoesNotWeakenSQLStateValidation(t *testing.T) {
	for _, codes := range []ErrorCodeFormat{SQLStateCodes, CQLCodes, DatabendCodes} {
		for _, value := range []string{"a123456789abcdef", "a123456789abcdeg", "a123456789abcde", "A123456789ABCDEF", "42000", "private diagnostic", "a123456789abcdef0123456789abcdef"} {
			raw, _ := json.Marshal(map[string]any{"code": -1, "message": "private native error", "data": map[string]any{
				"contractVersion": 1, "category": "sql", "stage": "execute", "sessionDisposition": "keep",
				"operationOutcome": "unknown", "retryable": false, "sqlState": value}})
			remote, err := decodeRemote(raw, true, "", codes)
			valid := codes == DatabendCodes && value == "a123456789abcdef" || codes != CQLCodes && value == "42000"
			if (err == nil) != valid {
				t.Fatal("Databend ID prefix accepted an incompatible diagnostic", codes, value, err)
			}
			if valid && value != "42000" && (remote.SQLState != "" || remote.NativeCode != "" || remote.NativeQueryIDPrefix != value || bytes.Contains([]byte(remote.Error()), []byte("private native error"))) {
				t.Fatal("query ID prefix became SQLSTATE, native code, or leaked error text")
			}
		}
	}
}

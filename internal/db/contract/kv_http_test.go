package contract

import (
	"math"
	"testing"
)

func TestHTTPReceiptClonePreservesOriginalBytesPresenceAndUnsignedIndices(t *testing.T) {
	index := uint64(math.MaxUint64)
	r := KVResult{HTTP: &KVHTTPResult{Request: KVHTTPRequest{Method: "PUT", Path: "/v1/kv/key", Query: map[string][]string{"cas": {"0"}}, Body: []byte{0, 255}},
		Header: map[string][]string{"X-Original": {"retained"}}, Trailer: map[string][]string{"X-End": {"complete"}}, Body: []byte{255, 0},
		Metadata: KVHTTPMetadata{Index: &index}, Entries: []KVHTTPEntry{{Key: "key", PresentFields: []string{"Flags", "Key", "Value"}, Flags: math.MaxUint64, ValuePresent: true, Value: []byte{0, 255}}},
		Txn: &KVHTTPTransaction{Results: []KVHTTPEntry{{Value: []byte{0}}}, Errors: []KVHTTPTransactionError{{What: "original"}}}}}
	copy := r.Clone()
	copy.HTTP.Request.Body[0] = 9
	copy.HTTP.Request.Query["cas"][0] = "1"
	copy.HTTP.Header["X-Original"][0] = "changed"
	copy.HTTP.Trailer["X-End"][0] = "changed"
	copy.HTTP.Body[0] = 9
	*copy.HTTP.Metadata.Index = 1
	copy.HTTP.Entries[0].Value[0] = 9
	copy.HTTP.Entries[0].PresentFields[0] = "changed"
	copy.HTTP.Txn.Results[0].Value[0] = 9
	copy.HTTP.Txn.Errors[0].What = "changed"
	if r.HTTP.Request.Body[0] != 0 || r.HTTP.Request.Query["cas"][0] != "0" || r.HTTP.Header["X-Original"][0] != "retained" || r.HTTP.Trailer["X-End"][0] != "complete" || r.HTTP.Body[0] != 255 || *r.HTTP.Metadata.Index != math.MaxUint64 || r.HTTP.Entries[0].Value[0] != 0 || r.HTTP.Entries[0].PresentFields[0] != "Flags" || r.HTTP.Txn.Results[0].Value[0] != 0 || r.HTTP.Txn.Errors[0].What != "original" {
		t.Fatal("HTTP inspection aliases the original receipt")
	}
	if r.NativeCaptured || r.Native != nil || r.Header != (KVHeader{}) || r.Reply != nil {
		t.Fatal("HTTP values invented a protobuf/etcd projection")
	}
	r.HTTP.Entries[0].Value = []byte{}
	r.HTTP.Entries[0].ValueNull = false
	if r.Clone().HTTP.Entries[0].Value == nil || !r.Clone().HTTP.Entries[0].ValuePresent {
		t.Fatal("explicit empty value became absent")
	}
	recovery := KVRecovery{Last: KVResult{HTTP: &KVHTTPResult{Body: []byte("read")}}, Unknown: &r}
	v := recovery.Clone()
	v.Unknown.HTTP.Request.Body[0] = 3
	v.Last.HTTP.Body[0] = 'x'
	if recovery.Unknown.HTTP.Request.Body[0] != 0 || string(recovery.Last.HTTP.Body) != "read" {
		t.Fatal("recovery changed original independent receipts")
	}
}

package contract

import "testing"

func TestKVScopeAndReceiptSnapshotsOwnEveryByteSlice(t *testing.T) {
	scope := KVScope{Prefix: []byte{0, 255}}
	copy := scope.Clone()
	copy.Prefix[0] = 1
	if scope.Prefix[0] != 0 {
		t.Fatal("prefix alias")
	}
	result := KVResult{Native: []byte{1, 2}, Reply: &KVReply{Entries: []KVEntry{{Key: []byte{1}, Value: []byte{2}}}, Operations: []KVReply{{Lease: &KVLeaseReceipt{Keys: [][]byte{{3}}, IDs: []int64{4}}, Status: &KVStatus{Errors: []string{"original"}}}}}}
	snapshot := result.Clone()
	snapshot.Native[0] = 2
	snapshot.Reply.Entries[0].Value[0] = 3
	snapshot.Reply.Operations[0].Lease.Keys[0][0] = 4
	snapshot.Reply.Operations[0].Lease.IDs[0] = 5
	snapshot.Reply.Operations[0].Status.Errors[0] = "changed"
	if result.Native[0] != 1 || result.Reply.Entries[0].Value[0] != 2 || result.Reply.Operations[0].Lease.Keys[0][0] != 3 || result.Reply.Operations[0].Lease.IDs[0] != 4 || result.Reply.Operations[0].Status.Errors[0] != "original" {
		t.Fatal("native receipt snapshot alias")
	}
}

package contract

import "testing"

func TestAlarmResultClonePreservesUnsignedUnknownEvidenceWithoutAliases(t *testing.T) {
	original := KVResult{Native: []byte{1, 2}, Reply: &KVReply{Kind: "alarm", Alarms: []KVAlarm{{MemberID: ^uint64(0), Type: -2147483648, NativeExtensions: []byte{0xa0, 6, 1}}}}}
	clone := original.Clone()
	clone.Native[0] = 0
	clone.Reply.Alarms[0].MemberID = 0
	clone.Reply.Alarms[0].Type = 0
	clone.Reply.Alarms[0].NativeExtensions[0] = 0
	if original.Native[0] != 1 || original.Reply.Alarms[0].MemberID != ^uint64(0) || original.Reply.Alarms[0].Type != -2147483648 || original.Reply.Alarms[0].NativeExtensions[0] != 0xa0 {
		t.Fatal("complete original alarm result aliases clone")
	}
}

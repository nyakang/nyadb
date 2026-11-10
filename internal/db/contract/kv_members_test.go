package contract

import "testing"

func TestKVMemberReceiptsOwnCompleteDirectoryBeforeAndUnknownBytes(t *testing.T) {
	m := KVMember{ID: ^uint64(0), PeerURLs: []string{"peer"}, ClientURLs: []string{"original"}, NativeExtensions: []byte{7}}
	result := KVMemberResult{
		Scope: KVMemberScope{Original: KVScope{Prefix: []byte{0, 255}, Generation: 19}, Member: m,
			Directory: KVMemberDirectory{Members: []KVMember{m}, Result: KVResult{Native: []byte{1}, NativeCaptured: true}},
			Handshake: KVResult{Native: []byte{2}}},
		Before: KVResult{Native: []byte{3}},
		Result: KVResult{Native: []byte{4}, WriteOutcomeUnknown: true},
	}
	copy := result.Clone()
	copy.Scope.Original.Prefix[0] = 5
	copy.Scope.Member.ClientURLs[0] = "other"
	copy.Scope.Directory.Members[0].PeerURLs[0] = "other"
	copy.Scope.Directory.Members[0].NativeExtensions[0] = 5
	copy.Scope.Directory.Result.Native[0] = 5
	copy.Scope.Handshake.Native[0] = 5
	copy.Before.Native[0] = 5
	copy.Result.Native[0] = 5
	if result.Scope.Original.Prefix[0] != 0 || result.Scope.Member.ClientURLs[0] != "original" || result.Scope.Directory.Members[0].PeerURLs[0] != "peer" || result.Scope.Directory.Members[0].NativeExtensions[0] != 7 || result.Scope.Directory.Result.Native[0] != 1 || result.Scope.Handshake.Native[0] != 2 || result.Before.Native[0] != 3 || result.Result.Native[0] != 4 || copy.Scope.Original.Generation != 19 || !copy.Result.WriteOutcomeUnknown || copy.Scope.Member.ID != ^uint64(0) {
		t.Fatal("copied receipt changed original evidence or lost full unsigned identity")
	}
}

package contract

import "testing"

func TestKVHistoryCloneOwnsOriginalRequestsReceiptsAndBothEventProjections(t *testing.T) {
	e := KVEvent{Type: "DELETE", Entry: KVEntry{Key: []byte{0, 255}, ModRevision: 9223372036854775807}, Previous: &KVEntry{Key: []byte{0, 255}, Value: []byte{1}, Lease: 9223372036854775807}}
	r := KVHistoryResult{Authored: KVHistoryRequest{Key: []byte{0, 255}}, Window: KVHistoryRequest{Key: []byte{0, 255}}, Initial: KVResult{Native: []byte{2}}, Frames: []KVEventFrame{{Native: []byte{3}, Events: []KVEvent{e}}}, Events: []KVEvent{e}}
	c := r.Clone()
	c.Authored.Key[0], c.Window.Key[0], c.Initial.Native[0], c.Frames[0].Native[0] = 9, 9, 9, 9
	c.Events[0].Previous.Value[0] = 9
	c.Frames[0].Events[0].Entry.Key[0] = 9
	if r.Authored.Key[0] != 0 || r.Window.Key[0] != 0 || r.Initial.Native[0] != 2 || r.Frames[0].Native[0] != 3 || r.Events[0].Previous.Value[0] != 1 || c.Frames[0].Events[0].Previous.Value[0] != 1 || r.Events[0].Entry.ModRevision != 9223372036854775807 || c.Events[0].Previous.Lease != 9223372036854775807 {
		t.Fatal("native history receipt aliases or loses exact metadata")
	}
}

package contract

import "testing"

func TestLeaseBrowserCopiesOriginalBoundaryReceiptsAndCompleteBinaryKeys(t *testing.T) {
	id := int64(9007199254740993)
	page := KVLeasePage{Scope: KVScope{Prefix: []byte{0, 255}}, Request: KVLeasePageRequest{Limit: 100, After: &id}, NextAfter: &id, List: KVResult{Native: []byte{0, 255}}, Rows: []KVLeaseObservation{{ID: id, Result: KVResult{Native: []byte{1, 255}, Reply: &KVReply{Lease: &KVLeaseReceipt{ID: id, Keys: [][]byte{{0, 255}}}}}}}, Complete: true}
	clone := page.Clone()
	clone.Scope.Prefix[0] = 9
	*clone.Request.After = 9
	*clone.NextAfter = 9
	clone.List.Native[0] = 9
	clone.Rows[0].Result.Native[0] = 9
	clone.Rows[0].Result.Reply.Lease.Keys[0][0] = 9
	if page.Scope.Prefix[0] != 0 || *page.Request.After != id || *page.NextAfter != id || page.List.Native[0] != 0 || page.Rows[0].Result.Native[0] != 1 || page.Rows[0].Result.Reply.Lease.Keys[0][0] != 0 {
		t.Fatal("page clone changed original request, scope or complete receipts")
	}
	detail := KVLeaseDetail{Scope: page.Scope, Request: KVLeaseDetailRequest{ID: id, IncludeKeys: true}, Result: page.Rows[0].Result}
	copied := detail.Clone()
	copied.Result.Reply.Lease.Keys[0][0] = 8
	if detail.Result.Reply.Lease.Keys[0][0] != 0 || copied.Request.ID != id || !copied.Request.IncludeKeys {
		t.Fatal("detail clone changed original binary keys or exact request")
	}
}

package contract

import "testing"

func TestKVRenewalBatchClonePreservesOriginalUnknownOutcomeAndCompleteBytes(t *testing.T) {
	b := KVRenewalBatch{Request: KVRenewalRequest{LeaseID: 9223372036854775807}, Receipts: []KVRenewalReceipt{{Native: []byte{0, 255}, WriteOutcomeUnknown: true, TTL: 9223372036854775807}}, Terminal: KVRenewalTerminal{WriteOutcomeUnknown: true}}
	c := b.Clone()
	c.Receipts[0].Native[0] = 7
	c.Receipts[0].WriteOutcomeUnknown = false
	if b.Receipts[0].Native[0] != 0 || !b.Receipts[0].WriteOutcomeUnknown || !c.Terminal.WriteOutcomeUnknown || c.Request != b.Request || c.Receipts[0].TTL != b.Receipts[0].TTL {
		t.Fatal("clone narrowed/aliased/reclassified original receipt")
	}
}

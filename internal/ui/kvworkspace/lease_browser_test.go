package kvworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyakang/nyadb/internal/db/contract"
)

type fakeBrowserLease struct {
	*fakeLease
	browse         func(context.Context, contract.KVLeasePageRequest) (contract.KVLeasePage, error)
	detail         func(context.Context, contract.KVLeaseDetailRequest) (contract.KVLeaseDetail, error)
	lists, details atomic.Int32
}

func (l *fakeBrowserLease) BrowseLeases(ctx context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
	l.lists.Add(1)
	if l.browse != nil {
		return l.browse(ctx, r)
	}
	return browserTestPage(l.Scope(), r), nil
}
func (l *fakeBrowserLease) ObserveLease(ctx context.Context, r contract.KVLeaseDetailRequest) (contract.KVLeaseDetail, error) {
	l.details.Add(1)
	if l.detail != nil {
		return l.detail(ctx, r)
	}
	result := browserTestTTL(r.ID)
	if r.IncludeKeys {
		result.Reply.Lease.Keys = [][]byte{{0, 255}, bytes.Repeat([]byte{255}, 4096)}
	}
	return contract.KVLeaseDetail{Scope: l.Scope(), Request: r, Result: result}, nil
}
func browserTestTTL(id int64) contract.KVResult {
	return contract.KVResult{Request: contract.KVRequest{Method: "/etcdserverpb.Lease/LeaseTimeToLive", JSON: `{"ID":"` + signedText(id) + `"}`}, Attempted: true, Completed: true, Native: []byte{0, 255}, JSON: `{"ID":"` + signedText(id) + `","TTL":"30"}`, Header: contract.KVHeader{ClusterID: math.MaxUint64, Revision: 9007199254740993}, Reply: &contract.KVReply{Kind: "lease_ttl", Lease: &contract.KVLeaseReceipt{ID: id, TTL: 30, GrantedTTL: 60}}}
}
func signedText(id int64) string { raw, _ := json.Marshal(id); return string(raw) }
func browserTestPage(scope contract.KVScope, r contract.KVLeasePageRequest) contract.KVLeasePage {
	all := []int64{math.MinInt64, 9007199254740993, math.MaxInt64}
	p := contract.KVLeasePage{Scope: scope, Request: r.Clone(), Complete: true, List: contract.KVResult{Attempted: true, Completed: true, Native: []byte{0, 255, 3}, JSON: `{"leases":[]}`, Reply: &contract.KVReply{Kind: "lease_list", Lease: &contract.KVLeaseReceipt{IDs: all}}}}
	for _, id := range all {
		if r.After == nil || id > *r.After {
			if len(p.Rows) < r.Limit {
				p.Rows = append(p.Rows, contract.KVLeaseObservation{ID: id, Result: browserTestTTL(id)})
			} else {
				p.More = true
			}
		}
	}
	if p.More {
		id := p.Rows[len(p.Rows)-1].ID
		p.NextAfter = &id
	}
	return p
}
func browserTestState(t *testing.T) (*State, *Document, *fakeBrowserLease) {
	t.Helper()
	l := &fakeBrowserLease{fakeLease: &fakeLease{scope: testScope()}}
	p := &fakeProvider{lease: l.fakeLease, reserve: func(uint64) (contract.KVRequestLease, error) { return l, nil }}
	s := New(Dependencies{ProviderFor: func(int64) (contract.KVRequestsProvider, error) { return p, nil }})
	d := s.Open(1, 7, DefaultLeaseBrowserRequest(LeaseBrowseMethod))
	t.Cleanup(s.CloseAll)
	return s, d, l
}
func TestKVLeaseBrowserExactSignedInputsAndRequestFileCompatibility(t *testing.T) {
	for _, text := range []string{`{}`, `{"limit":200,"after":"-9223372036854775808"}`, `{"after":9223372036854775807}`, `{"after":"0"}`} {
		if _, err := ParseLeasePageRequest(text); err != nil {
			t.Fatal(text, err)
		}
	}
	for _, text := range []string{`null`, `[]`, `{"limit":0}`, `{"limit":201}`, `{"limit":1.0}`, `{"after":null}`, `{"after":"9223372036854775808"}`, `{"after":1,"after":2}`, `{"continuation":"1"}`} {
		if _, err := ParseLeasePageRequest(text); err == nil {
			t.Fatal("accepted", text)
		}
	}
	for _, text := range []string{`{"ID":"-9223372036854775808","keys":true}`, `{"id":9223372036854775807}`, `{"ID":"9007199254740993"}`} {
		r, err := ParseLeaseDetailRequest(text)
		if err != nil || r.ID == 0 {
			t.Fatal(text, err)
		}
	}
	for _, text := range []string{`{}`, `{"ID":0}`, `{"ID":1,"id":1}`, `{"ID":1e3}`, `{"ID":"+1"}`, `{"ID":1,"keys":null}`, `{"ID":1,"keys":"true"}`, `{"ID":1,"key":true}`} {
		if _, err := ParseLeaseDetailRequest(text); err == nil {
			t.Fatal("accepted", text)
		}
	}
	for _, method := range []string{LeaseBrowseMethod, LeaseDetailMethod} {
		r := DefaultLeaseBrowserRequest(method)
		raw, err := EncodeRequest(r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRequest(raw)
		if err != nil || got != r {
			t.Fatal("request file changed")
		}
	}
}
func TestKVLeaseBrowserOptionalFacetAndIndependentOriginalReceipts(t *testing.T) {
	s0, d0, _ := testState(t)
	for _, m := range s0.MethodsFor(d0) {
		if leaseBrowserMethod(m.Path) {
			t.Fatal("ordinary lease advertised facet")
		}
	}
	s, d, l := browserTestState(t)
	d.last = &Receipt{Scope: testScope(), Authored: DefaultRequest(), Result: contract.KVResult{Native: []byte{7}}}
	d.unknown = &Receipt{Result: contract.KVResult{WriteOutcomeUnknown: true, Native: []byte{8}}}
	d.renewalUnknown = &RenewalReceipt{Batch: contract.KVRenewalBatch{Terminal: contract.KVRenewalTerminal{WriteOutcomeUnknown: true}}}
	original := d.last
	unknown := d.unknown
	d.Editor.Buffer.Set(`{"limit":2}`)
	s.Run(d)
	consume(t, s, d)
	if d.browserPage == nil || !d.browserPage.Page.Complete || !d.browserPage.Page.More || d.last != original || d.unknown != unknown || !d.hasUnknownWrites() {
		t.Fatal("browsing replaced original receipts", d.Error())
	}
	s.SelectLease(d, 9007199254740993)
	s.ReadSelectedLease(d)
	consume(t, s, d)
	if d.browserDetail == nil || d.browserDetail.Detail.Request.ID != 9007199254740993 || len(d.browserDetail.Detail.Result.Reply.Lease.Keys[1]) != 4096 || d.last != original {
		t.Fatal("detail changed ID/keys/original receipt")
	}
	r, _ := d.LeaseDetailReceipt()
	r.Detail.Result.Native[0] = 9
	r.Detail.Result.Reply.Lease.Keys[0][0] = 9
	r.Scope.Prefix[0] = 9
	if d.browserDetail.Detail.Result.Native[0] != 0 || d.browserDetail.Detail.Result.Reply.Lease.Keys[0][0] != 0 || d.scope.Prefix[0] != 0 {
		t.Fatal("receipt clone aliases original")
	}
	raw, err := s.LeaseBrowserRecord(d)
	if err != nil || !strings.Contains(string(raw), `"requested_id": "9007199254740993"`) || !strings.Contains(string(raw), `"cluster_id": "18446744073709551615"`) {
		t.Fatal("export rounded original values", err)
	}
	s.NextLeasePage(d)
	consume(t, s, d)
	if d.browserPage.Page.More || len(d.browserPage.Page.Rows) != 1 || d.browserPage.Page.Rows[0].ID != math.MaxInt64 {
		t.Fatal("next window lost signed boundary")
	}
	s.NextLeasePage(d)
	if d.Busy() || l.lists.Load() != 2 || l.executes.Load() != 0 {
		t.Fatal("browser replayed or executed one-shot")
	}
	s.ClearLeaseBrowser(d)
	if d.browserLast != nil || d.browserDetail != nil || d.browserPage != nil || d.unknown != unknown || d.renewalUnknown == nil {
		t.Fatal("clear discarded independent unknown")
	}
}
func TestKVLeaseBrowserParkedPartialCancellationWaitsAndKeepsCachedPage(t *testing.T) {
	s, d, l := browserTestState(t)
	s.Run(d)
	consume(t, s, d)
	original := d.browserPage
	entered, ack := make(chan struct{}), make(chan struct{})
	l.browse = func(ctx context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
		p := browserTestPage(l.Scope(), r)
		p.Complete = false
		p.Rows = p.Rows[:1]
		p.NextAfter = nil
		close(entered)
		<-ctx.Done()
		<-ack
		return p, ctx.Err()
	}
	s.Run(d)
	await(t, entered)
	job := d.job
	active := false
	s.deps.Active = func(*Document) bool { return active }
	s.RequestCancel(d)
	s.Consume()
	if !d.Busy() || !d.CancelRequested() {
		t.Fatal("cancellation did not wait for executor")
	}
	close(ack)
	await(t, job.finished)
	s.Consume()
	if d.job != job || d.browserPage != original {
		t.Fatal("parked completion consumed")
	}
	active = true
	s.Consume()
	if d.Busy() || d.browserPage != original || d.browserLast.Page.Complete || len(d.browserLast.Page.Rows) != 1 || d.browserLast.Error == "" {
		t.Fatal("partial receipt lost or replaced cache")
	}
	raw, err := s.LeaseBrowserRecord(d)
	if err != nil || !bytes.Contains(raw, []byte(`"complete": false`)) || !bytes.Contains(raw, []byte(`"native_protobuf": "AP8D"`)) {
		t.Fatal("partial full evidence missing")
	}
}
func TestKVLeaseBrowserOriginalGenerationAndMalformedRepliesNeverReplaceCache(t *testing.T) {
	for _, kind := range []string{"retired", "foreign-scope", "foreign-id", "duplicate-list", "bad-boundary", "expired-end", "foreign-kind"} {
		t.Run(kind, func(t *testing.T) {
			s, d, l := browserTestState(t)
			d.Editor.Buffer.Set(`{"limit":2}`)
			s.Run(d)
			consume(t, s, d)
			original := d.browserPage
			l.browse = func(_ context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
				p := browserTestPage(l.Scope(), r)
				switch kind {
				case "retired":
					l.lost.Store(true)
				case "foreign-scope":
					p.Scope.Generation++
				case "foreign-id":
					p.Rows[0].Result.Reply.Lease.ID++
				case "duplicate-list":
					p.List.Reply.Lease.IDs[1] = p.List.Reply.Lease.IDs[0]
				case "foreign-kind":
					p.Rows[0].Result.Reply.Kind = "lease_grant"
				case "bad-boundary":
					*p.NextAfter = 1
				case "expired-end":
					r.After = new(int64(math.MaxInt64))
					p = browserTestPage(l.Scope(), r)
				}
				return p, nil
			}
			s.Run(d)
			consume(t, s, d)
			if d.browserPage != original || d.browserLast == original || d.browserLast.Error == "" || d.Busy() {
				t.Fatal("bad/late result entered current cache")
			}
		})
	}
}
func TestKVLeaseBrowserRecoverRemainsPureAndCloseReleasesOriginalExecutorAndCaches(t *testing.T) {
	s, d, l := browserTestState(t)
	s.Run(d)
	consume(t, s, d)
	receipt := d.browserPage
	l.lost.Store(true)
	s.Consume()
	next := &fakeBrowserLease{fakeLease: &fakeLease{scope: testScope()}}
	next.scope.Generation++
	p := &fakeProvider{lease: next.fakeLease, recover: func(previous contract.KVRequestLease) (contract.KVRecovery, error) {
		if previous != l {
			t.Error("foreign old lease")
		}
		return contract.KVRecovery{Lease: next}, nil
	}}
	s.deps.ProviderFor = func(int64) (contract.KVRequestsProvider, error) { return p, nil }
	s.Recover(d)
	consume(t, s, d)
	if d.NeedsRecovery() || next.lists.Load() != 0 || next.details.Load() != 0 || d.browserPage != receipt {
		t.Fatal("recover reread or discarded original receipt")
	}
	s.SelectLease(d, 9007199254740993)
	s.ReadSelectedLease(d)
	if d.Busy() {
		t.Fatal("old selection transferred to new generation")
	}
	entered := make(chan struct{})
	next.browse = func(ctx context.Context, r contract.KVLeasePageRequest) (contract.KVLeasePage, error) {
		close(entered)
		<-ctx.Done()
		return browserTestPage(next.Scope(), r), errors.New("settled original cancellation")
	}
	s.Run(d)
	await(t, entered)
	s.Close(d)
	s.Close(d)
	if d.browserPage != nil || d.browserLast != nil || d.lease != nil || d.Editor != nil || !next.closed.Load() {
		t.Fatal("close retained original caches")
	}
}

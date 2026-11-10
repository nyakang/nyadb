package kvworkspace

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

type Method struct{ Name, Path string }

var methods = []Method{
	{"Range", "/etcdserverpb.KV/Range"}, {"Put", "/etcdserverpb.KV/Put"}, {"DeleteRange", "/etcdserverpb.KV/DeleteRange"}, {"Txn", "/etcdserverpb.KV/Txn"},
	{"Status", "/etcdserverpb.Maintenance/Status"}, {"LeaseGrant", "/etcdserverpb.Lease/LeaseGrant"}, {"LeaseRevoke", "/etcdserverpb.Lease/LeaseRevoke"}, {"LeaseTimeToLive", "/etcdserverpb.Lease/LeaseTimeToLive"}, {"LeaseLeases", "/etcdserverpb.Lease/LeaseLeases"}, {"LeaseKeepAlive", "/etcdserverpb.Lease/LeaseKeepAlive"},
}

func Methods() []Method { return append([]Method(nil), methods...) }
func (s *State) MethodsFor(d *Document) []Method {
	out := Methods()
	if s.owned(d) && d.MembersAvailable() {
		out = append(out, Method{"Members", MembersMethod})
	}
	if s.owned(d) && d.CompactionAvailable() {
		out = append(out, Method{"Compact", CompactMethod})
	}
	if s.owned(d) && d.AuthManagementAvailable() {
		out = append(out, Method{Name: "AuthManagement", Path: AuthManagementMethod})
	}
	if s.owned(d) && d.LeaseBrowsingAvailable() {
		out = append(out, Method{"LeaseBrowse", LeaseBrowseMethod}, Method{"LeaseDetail", LeaseDetailMethod})
	}
	if s.owned(d) && !d.eventsAvailable && s.deps.EventsProviderFor != nil {
		if p, err := s.deps.EventsProviderFor(d.profileID); err == nil && p != nil && p.KVScope().Namespace.ProfileID == d.profileID {
			d.eventsAvailable = true
		}
	}
	if s.owned(d) && (d.eventsAvailable || d.eventLease != nil) {
		out = append(out, Method{"Watch", WatchMethod}, Method{"History", HistoryMethod})
	}
	if s.owned(d) && !d.renewalAvailable && s.deps.RenewalProviderFor != nil {
		if p, err := s.deps.RenewalProviderFor(d.profileID); err == nil && p != nil && p.KVScope().Namespace.ProfileID == d.profileID {
			d.renewalAvailable = true
		}
	}
	if s.owned(d) && (d.renewalAvailable || d.renewalLease != nil) {
		out = append(out, Method{Name: "LeaseRenewal", Path: RenewalMethod})
	}
	return out
}
func DefaultEventRequest(method string) contract.KVRequest {
	if method == WatchMethod {
		return contract.KVRequest{Method: method, JSON: "{\n  \"key\": \"AA==\",\n  \"start_revision\": \"0\",\n  \"watch_id\": \"1\",\n  \"prev_kv\": true\n}"}
	}
	if method == HistoryMethod {
		return contract.KVRequest{Method: method, JSON: "{\n  \"key\": \"AA==\",\n  \"start_revision\": \"0\",\n  \"end_revision\": \"0\",\n  \"limit\": 100\n}"}
	}
	return DefaultRequest()
}
func (s *State) ChooseMethod(d *Document, method string) {
	if !s.owned(d) || d.Busy() {
		return
	}
	before := DefaultRequest()
	if d.method == MembersMethod {
		before = DefaultMembersRequest()
	}
	if d.method == CompactMethod {
		before = DefaultCompactionRequest()
	}
	if d.method == AuthManagementMethod {
		before = DefaultAuthManagementRequest()
	}
	if d.method == RenewalMethod {
		before = DefaultRenewalRequest()
	}
	if eventMethod(d.method) {
		before = DefaultEventRequest(d.method)
	}
	if leaseBrowserMethod(d.method) {
		before = DefaultLeaseBrowserRequest(d.method)
	}
	useTemplate := d.Request().JSON == before.JSON
	s.SetMethod(d, method)
	if useTemplate && method == MembersMethod {
		d.Editor.Buffer.Set(DefaultMembersRequest().JSON)
	} else if useTemplate && method == CompactMethod {
		d.Editor.Buffer.Set(DefaultCompactionRequest().JSON)
	} else if useTemplate && method == AuthManagementMethod {
		d.Editor.Buffer.Set(DefaultAuthManagementRequest().JSON)
	} else if useTemplate && leaseBrowserMethod(method) {
		d.Editor.Buffer.Set(DefaultLeaseBrowserRequest(method).JSON)
	} else if useTemplate && method == RenewalMethod {
		d.Editor.Buffer.Set(DefaultRenewalRequest().JSON)
	} else if useTemplate && eventMethod(method) {
		d.Editor.Buffer.Set(DefaultEventRequest(method).JSON)
	} else if useTemplate && method == DefaultRequest().Method {
		d.Editor.Buffer.Set(DefaultRequest().JSON)
	}
}

// RequestForKey keeps an absolute original byte key and a fixed page revision.
func RequestForKey(read contract.KVKeyRead, history bool) (contract.KVRequest, error) {
	if len(read.Key) == 0 || len(read.Key) > 4096 || read.Revision <= 0 || !history && read.Revision == math.MaxInt64 {
		return contract.KVRequest{}, jsondocument.ErrInvalid
	}
	fields := map[string]any{"key": base64.StdEncoding.EncodeToString(read.Key)}
	method := HistoryMethod
	if history {
		fields["start_revision"] = "0"
		fields["end_revision"] = strconv.FormatInt(read.Revision, 10)
		fields["limit"] = 100
	} else {
		method = WatchMethod
		fields["start_revision"] = strconv.FormatInt(read.Revision+1, 10)
		fields["watch_id"] = "1"
		fields["prev_kv"] = true
	}
	raw, err := json.MarshalIndent(fields, "", "  ")
	return contract.KVRequest{Method: method, JSON: string(raw)}, err
}
func DefaultRequest() contract.KVRequest {
	return contract.KVRequest{Method: "/etcdserverpb.KV/Range", JSON: "{\n  \"key\": \"AA==\",\n  \"limit\": \"100\"\n}"}
}

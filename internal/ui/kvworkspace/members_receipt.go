package kvworkspace

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Native identities use decimal strings throughout this archive. Typed member
// properties supplement, and never replace, complete original protobuf/JSON.
type memberRecord struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	PeerURLs   []string `json:"peer_urls"`
	ClientURLs []string `json:"client_urls"`
	Learner    bool     `json:"is_learner"`
	Extensions []byte   `json:"native_extensions"`
}
type memberDirectoryRecord struct {
	Document string           `json:"original_document_id"`
	Scope    leaseScopeRecord `json:"original_scope"`
	Result   json.RawMessage  `json:"original_member_list_receipt"`
	Members  []memberRecord   `json:"members"`
	Error    string           `json:"error"`
}
type memberScopeRecord struct {
	Original  leaseScopeRecord       `json:"original_scope"`
	Document  string                 `json:"original_document_id"`
	Lease     string                 `json:"original_member_lease_id"`
	Directory *memberDirectoryRecord `json:"original_directory"`
	Member    memberRecord           `json:"original_member"`
	Endpoint  string                 `json:"explicit_endpoint"`
	Version   string                 `json:"independent_expected_version"`
	Handshake json.RawMessage        `json:"original_status_handshake"`
}
type memberResultRecord struct {
	Scope       *memberScopeRecord     `json:"original_member_scope"`
	Sequence    string                 `json:"sequence"`
	Directory   *memberDirectoryRecord `json:"directory_check"`
	Before      json.RawMessage        `json:"before_status"`
	BeforeError string                 `json:"before_error"`
	Result      json.RawMessage        `json:"original_native_receipt"`
	Error       string                 `json:"error"`
}
type memberBindingRecord struct {
	Target    memberTargetText   `json:"authored_target"`
	Scope     *memberScopeRecord `json:"original_member_scope"`
	Attempted bool               `json:"binding_attempted"`
	Error     string             `json:"error"`
}
type memberBatchTargetRecord struct {
	Scope   *memberScopeRecord  `json:"original_member_scope"`
	Request requestRecord       `json:"empty_defragment_request"`
	State   string              `json:"state"`
	Result  *memberResultRecord `json:"result"`
}
type memberRoundRecord struct {
	Remaining []string              `json:"remaining_original_member_ids"`
	Statuses  []*memberResultRecord `json:"original_ordering_observations"`
	Chosen    string                `json:"chosen_original_member_id"`
	Error     string                `json:"error"`
}
type memberBatchRecord struct {
	Document string                    `json:"original_document_id"`
	Scope    leaseScopeRecord          `json:"original_scope"`
	Authored requestRecord             `json:"authored_selection"`
	Targets  []memberBatchTargetRecord `json:"targets"`
	Rounds   []memberRoundRecord       `json:"ordering_rounds"`
	Ended    bool                      `json:"ended"`
	Error    string                    `json:"error"`
}
type membersArchive struct {
	Directory           *contract.KVMemberDirectory
	History             []MemberBindingReceipt
	Statuses, Recovered []contract.KVMemberResult
	Batch               *MemberBatchReceipt
	Unknown             *contract.KVMemberResult
}
type membersArchiveRecord struct {
	Format    string                 `json:"format"`
	Directory *memberDirectoryRecord `json:"latest_original_directory"`
	History   []memberBindingRecord  `json:"original_binding_history"`
	Statuses  []*memberResultRecord  `json:"latest_original_member_statuses"`
	Recovered []*memberResultRecord  `json:"original_recovered_evidence"`
	Batch     *memberBatchRecord     `json:"last_checked_batch"`
	Unknown   *memberResultRecord    `json:"independent_unknown_write_receipt"`
	Semantics string                 `json:"semantics"`
}

func memberArchiveFor(d *Document) membersArchive {
	m := d.members
	if m == nil {
		return membersArchive{}
	}
	a := membersArchive{History: make([]MemberBindingReceipt, len(m.history)), Statuses: cloneMemberResults(m.statuses), Recovered: cloneMemberResults(m.recovered)}
	for i := range m.history {
		a.History[i] = m.history[i].Clone()
	}
	if m.directory != nil {
		r := m.directory.Clone()
		a.Directory = &r
	}
	if m.batch != nil {
		r := m.batch.Clone()
		a.Batch = &r
	}
	if m.unknown != nil {
		r := m.unknown.Clone()
		a.Unknown = &r
	}
	return a
}

func marshalMemberPreview(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
func memberData(m contract.KVMember) memberRecord {
	return memberRecord{strconv.FormatUint(m.ID, 10), m.Name, m.PeerURLs, m.ClientURLs, m.IsLearner, m.NativeExtensions}
}
func memberNativeData(scope contract.KVScope, result contract.KVResult, err string) (json.RawMessage, error) {
	raw, e := encodeOperationReceipt(Receipt{Scope: scope, Authored: result.Request, Result: result, Error: err})
	return json.RawMessage(raw), e
}
func memberDirectoryData(d *contract.KVMemberDirectory) (*memberDirectoryRecord, error) {
	if d == nil {
		return nil, nil
	}
	raw, err := memberNativeData(d.Scope, d.Result, d.Error)
	if err != nil {
		return nil, err
	}
	v := &memberDirectoryRecord{Document: strconv.FormatUint(d.DocumentID, 10), Scope: leaseScope(d.Scope), Result: raw, Error: d.Error}
	for _, m := range d.Members {
		v.Members = append(v.Members, memberData(m))
	}
	return v, nil
}
func memberScopeData(s contract.KVMemberScope) (*memberScopeRecord, error) {
	d, err := memberDirectoryData(&s.Directory)
	if err != nil {
		return nil, err
	}
	h, err := memberNativeData(s.Original, s.Handshake, "")
	if err != nil {
		return nil, err
	}
	return &memberScopeRecord{Original: leaseScope(s.Original), Document: strconv.FormatUint(s.DocumentID, 10), Lease: strconv.FormatUint(s.LeaseID, 10), Directory: d, Member: memberData(s.Member), Endpoint: s.Endpoint, Version: s.Version, Handshake: h}, nil
}
func memberResultData(r *contract.KVMemberResult) (*memberResultRecord, error) {
	if r == nil {
		return nil, nil
	}
	s, err := memberScopeData(r.Scope)
	if err != nil {
		return nil, err
	}
	d, err := memberDirectoryData(&r.DirectoryCheck)
	if err != nil {
		return nil, err
	}
	b, err := memberNativeData(r.Scope.Original, r.Before, r.BeforeError)
	if err != nil {
		return nil, err
	}
	v, err := memberNativeData(r.Scope.Original, r.Result, r.Error)
	if err != nil {
		return nil, err
	}
	return &memberResultRecord{Scope: s, Sequence: strconv.FormatUint(r.Sequence, 10), Directory: d, Before: b, BeforeError: r.BeforeError, Result: v, Error: r.Error}, nil
}
func memberResultsData(rs []contract.KVMemberResult) ([]*memberResultRecord, error) {
	var out []*memberResultRecord
	for _, r := range rs {
		v, err := memberResultData(&r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func memberBatchData(b *MemberBatchReceipt) (*memberBatchRecord, error) {
	if b == nil {
		return nil, nil
	}
	v := &memberBatchRecord{Document: strconv.FormatUint(b.DocumentID, 10), Scope: leaseScope(b.Scope), Authored: requestRecord{b.Authored.Method, b.Authored.JSON}, Ended: b.Ended, Error: b.Error}
	for _, t := range b.Targets {
		s, err := memberScopeData(t.Scope)
		if err != nil {
			return nil, err
		}
		r, err := memberResultData(t.Result)
		if err != nil {
			return nil, err
		}
		v.Targets = append(v.Targets, memberBatchTargetRecord{s, requestRecord{t.Request.Method, t.Request.JSON}, t.State, r})
	}
	for _, r := range b.Rounds {
		statuses, err := memberResultsData(r.Statuses)
		if err != nil {
			return nil, err
		}
		round := memberRoundRecord{Statuses: statuses, Chosen: strconv.FormatUint(r.Chosen, 10), Error: r.Error}
		for _, id := range r.Remaining {
			round.Remaining = append(round.Remaining, strconv.FormatUint(id, 10))
		}
		v.Rounds = append(v.Rounds, round)
	}
	return v, nil
}
func encodeMembersArchive(a membersArchive) ([]byte, error) {
	v := membersArchiveRecord{Format: "nyadb-original-members-v1", Semantics: "Directory DTOs and files grant no authority. Each step checks the original member metadata and transport; leader observations are not CAS protection. Execute followers before the selected leader, stop on the first failure/unknown, never rollback/replay. Later reads or recovery do not settle an unknown original ACK. IDs are observations, not incarnations. NativeCaptured distinguishes a valid empty protobuf ACK from absent capture."}
	var err error
	v.Directory, err = memberDirectoryData(a.Directory)
	if err != nil {
		return nil, err
	}
	for _, r := range a.History {
		s, err := memberScopeData(r.Scope)
		if err != nil {
			return nil, err
		}
		v.History = append(v.History, memberBindingRecord{memberTargetText{strconv.FormatUint(r.Target.MemberID, 10), r.Target.Endpoint, r.Target.Version}, s, r.Attempted, r.Error})
	}
	v.Statuses, err = memberResultsData(a.Statuses)
	if err != nil {
		return nil, err
	}
	v.Recovered, err = memberResultsData(a.Recovered)
	if err != nil {
		return nil, err
	}
	v.Batch, err = memberBatchData(a.Batch)
	if err != nil {
		return nil, err
	}
	v.Unknown, err = memberResultData(a.Unknown)
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if len(raw) > memberArchiveLimit {
		return nil, jsondocument.ErrBounds
	}
	return raw, err
}
func memberArchiveRoom(a membersArchive, bindings []boundMember, batch *MemberBatchReceipt, reserve int) bool {
	// Local slice replacement never mutates the captured archive's backing array.
	if len(bindings) != 0 {
		history := make([]MemberBindingReceipt, len(a.History), len(a.History)+len(bindings))
		copy(history, a.History)
		for _, b := range bindings {
			history = append(history, b.receipt)
		}
		a.History = history
	}
	if batch != nil {
		a.Batch = batch
	}
	raw, err := encodeMembersArchive(a)
	return err == nil && reserve >= 0 && len(raw) <= memberArchiveLimit-reserve
}
func (s *State) MembersRecord(d *Document) ([]byte, error) {
	if !s.owned(d) {
		return nil, jsondocument.ErrInvalid
	}
	return encodeMembersArchive(memberArchiveFor(d))
}
func (s *State) ExportMembersRecord(d *Document) {
	if !s.active(d) || d.FileBusy() || !d.memberContent() || s.deps.Files.SavePath == nil || s.deps.Files.WriteStream == nil {
		return
	}
	raw, err := s.MembersRecord(d)
	if err != nil {
		d.error = err.Error()
		return
	}
	f := s.deps.Files
	s.fileJob(d, "export", func(ctx context.Context) fileResult {
		path, err := f.SavePath("members.nyakv-receipt.json")
		if err != nil || path == "" {
			return fileResult{err: err}
		}
		return fileResult{path: path, err: writeCaptured(ctx, f, path, raw)}
	})
}

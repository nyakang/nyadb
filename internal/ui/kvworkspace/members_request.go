package kvworkspace

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
	"github.com/nyakang/nyadb/locales"
)

// This document method is an authored selection, never native member authority.
const MembersMethod = "nyadb://etcd/members"

func DefaultMembersRequest() contract.KVRequest {
	return contract.KVRequest{Method: MembersMethod, JSON: "{\n  \"targets\": []\n}"}
}

type memberTargetText struct {
	ID       string `json:"member_id"`
	Endpoint string `json:"endpoint"`
	Version  string `json:"version"`
}

func memberTargets(r contract.KVRequest) ([]contract.KVMemberTarget, error) {
	return parseMemberTargets(r, true)
}

func parseMemberTargets(r contract.KVRequest, complete bool) ([]contract.KVMemberTarget, error) {
	var fields map[string]json.RawMessage
	if r.Method != MembersMethod || jsondocument.Decode([]byte(r.JSON), &fields, inputLimit) != nil || len(fields) != 1 || fields["targets"] == nil || string(fields["targets"]) == "null" {
		return nil, jsondocument.ErrInvalid
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(fields["targets"], &entries) != nil || len(entries) > 8 {
		return nil, jsondocument.ErrInvalid
	}
	out := make([]contract.KVMemberTarget, 0, len(entries))
	seen := map[uint64]bool{}
	for _, entry := range entries {
		var text memberTargetText
		if len(entry) != 3 || json.Unmarshal(entry["member_id"], &text.ID) != nil || json.Unmarshal(entry["endpoint"], &text.Endpoint) != nil || json.Unmarshal(entry["version"], &text.Version) != nil || text.ID == "" || len(text.ID) > 20 || strings.Trim(text.ID, "0123456789") != "" || complete && (text.Endpoint == "" || text.Version == "") || len(text.Endpoint) > 4096 || len(text.Version) > 128 {
			return nil, jsondocument.ErrInvalid
		}
		id, err := strconv.ParseUint(text.ID, 10, 64)
		if err != nil || id == 0 || seen[id] {
			return nil, jsondocument.ErrInvalid
		}
		seen[id] = true
		out = append(out, contract.KVMemberTarget{MemberID: id, Endpoint: text.Endpoint, Version: text.Version})
	}
	return out, nil
}

func (s *State) SetMemberTarget(d *Document, target contract.KVMemberTarget, selected bool) {
	if !s.owned(d) || d.Busy() || d.method != MembersMethod {
		return
	}
	targets, err := parseMemberTargets(d.Request(), false)
	if err != nil {
		d.error = locales.Text("成员选择 JSON 无效")
		return
	}
	entries := make([]memberTargetText, 0, len(targets)+1)
	for _, old := range targets {
		if old.MemberID != target.MemberID {
			entries = append(entries, memberTargetText{strconv.FormatUint(old.MemberID, 10), old.Endpoint, old.Version})
		}
	}
	if selected {
		entries = append(entries, memberTargetText{strconv.FormatUint(target.MemberID, 10), target.Endpoint, target.Version})
	}
	raw, _ := json.MarshalIndent(struct {
		Targets []memberTargetText `json:"targets"`
	}{entries}, "", "  ")
	d.Editor.Buffer.Set(string(raw))
	d.confirmation, d.permit = nil, nil
}

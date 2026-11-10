package kvworkspace

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const LeaseBrowseMethod = "/nyadb.Lease/Browse"
const LeaseDetailMethod = "/nyadb.Lease/Detail"

func leaseBrowserMethod(method string) bool {
	return method == LeaseBrowseMethod || method == LeaseDetailMethod
}
func DefaultLeaseBrowserRequest(method string) contract.KVRequest {
	if method == LeaseDetailMethod {
		return contract.KVRequest{Method: method, JSON: "{\n  \"ID\": \"0\",\n  \"keys\": true\n}"}
	}
	return contract.KVRequest{Method: LeaseBrowseMethod, JSON: "{\n  \"limit\": 100\n}"}
}
func signedLeaseInt(raw json.RawMessage) (int64, error) {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &text) != nil {
		return 0, jsondocument.ErrInvalid
	}
	digits := strings.TrimPrefix(text, "-")
	if digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
		return 0, jsondocument.ErrInvalid
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, jsondocument.ErrInvalid
	}
	return id, nil
}
func ParseLeasePageRequest(text string) (contract.KVLeasePageRequest, error) {
	r := contract.KVLeasePageRequest{Limit: 100}
	f, err := eventFields(text, "limit", "after")
	if err != nil {
		return r, err
	}
	if raw := f["limit"]; raw != nil {
		limit, err := signedLeaseInt(raw)
		if err != nil || limit < 1 || limit > 200 {
			return r, jsondocument.ErrInvalid
		}
		r.Limit = int(limit)
	}
	if raw := f["after"]; raw != nil {
		id, err := signedLeaseInt(raw)
		if err != nil {
			return r, err
		}
		r.After = &id
	}
	return r, nil
}
func ParseLeaseDetailRequest(text string) (contract.KVLeaseDetailRequest, error) {
	var f map[string]json.RawMessage
	var r contract.KVLeaseDetailRequest
	if jsondocument.Decode([]byte(text), &f, inputLimit) != nil || f == nil {
		return r, jsondocument.ErrInvalid
	}
	for key, raw := range f {
		if key != "ID" && key != "id" && key != "keys" || string(raw) == "null" {
			return r, jsondocument.ErrInvalid
		}
	}
	if f["ID"] != nil && f["id"] != nil {
		return r, jsondocument.ErrInvalid
	}
	raw := f["ID"]
	if raw == nil {
		raw = f["id"]
	}
	id, err := signedLeaseInt(raw)
	if err != nil || id == 0 {
		return r, jsondocument.ErrInvalid
	}
	r.ID = id
	r.IncludeKeys, err = eventBool(f, "keys")
	return r, err
}

package kvworkspace

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Continuous authorization is a separate document method from one-shot KeepAlive.
const RenewalMethod = "/nyadb.Lease/Renewal"

func DefaultRenewalRequest() contract.KVRequest {
	return contract.KVRequest{Method: RenewalMethod, JSON: "{\n  \"ID\": \"0\"\n}"}
}
func ParseRenewalRequest(text string) (contract.KVRenewalRequest, error) {
	var f map[string]json.RawMessage
	if jsondocument.Decode([]byte(text), &f, inputLimit) != nil || len(f) != 1 {
		return contract.KVRenewalRequest{}, jsondocument.ErrInvalid
	}
	raw := f["ID"]
	if raw == nil {
		raw = f["id"]
	}
	if len(raw) == 0 {
		return contract.KVRenewalRequest{}, jsondocument.ErrInvalid
	}
	value := string(raw)
	if raw[0] == '"' && json.Unmarshal(raw, &value) != nil {
		return contract.KVRenewalRequest{}, jsondocument.ErrInvalid
	}
	digits := strings.TrimPrefix(value, "-")
	if digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
		return contract.KVRenewalRequest{}, jsondocument.ErrInvalid
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id == 0 {
		return contract.KVRenewalRequest{}, jsondocument.ErrInvalid
	}
	return contract.KVRenewalRequest{LeaseID: id}, nil
}

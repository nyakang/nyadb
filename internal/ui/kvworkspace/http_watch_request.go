package kvworkspace

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const HTTPWatchMethod = "/nyadb.KV/HTTPWatch"

func DefaultHTTPWatchRequest() contract.KVRequest {
	return contract.KVRequest{Method: HTTPWatchMethod, JSON: "{\n  \"target\": \"\",\n  \"prefix\": true,\n  \"observation_prefix\": \"\",\n  \"start_index\": \"0\",\n  \"wait\": \"1s\",\n  \"max_observations\": 32,\n  \"max_bytes\": \"16777216\"\n}"}
}

// Every field is authored explicitly. In particular an omitted actual range
// must never become an implicit root read, and uint64 never passes float64.
func ParseHTTPWatchRequest(text string) (contract.KVHTTPWatchRequest, error) {
	var r contract.KVHTTPWatchRequest
	f, err := eventFields(text, "target", "prefix", "observation_prefix", "start_index", "wait", "max_observations", "max_bytes")
	if err != nil || len(f) != 7 {
		return r, jsondocument.ErrInvalid
	}
	for key, dst := range map[string]*string{"target": &r.Target, "observation_prefix": &r.ObservationPrefix} {
		if json.Unmarshal(f[key], dst) != nil {
			return r, jsondocument.ErrInvalid
		}
	}
	if json.Unmarshal(f["prefix"], &r.Prefix) != nil {
		return r, jsondocument.ErrInvalid
	}
	var wait string
	if json.Unmarshal(f["wait"], &wait) != nil {
		return r, jsondocument.ErrInvalid
	}
	if r.Wait, err = time.ParseDuration(wait); err != nil {
		return r, jsondocument.ErrInvalid
	}
	unsigned := func(raw json.RawMessage) (uint64, error) {
		s := string(raw)
		if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) != nil {
			return 0, jsondocument.ErrInvalid
		}
		return strconv.ParseUint(s, 10, 64)
	}
	if r.StartIndex, err = unsigned(f["start_index"]); err != nil {
		return r, jsondocument.ErrInvalid
	}
	count, err := unsigned(f["max_observations"])
	if err != nil || count < 1 || count > 256 {
		return r, jsondocument.ErrInvalid
	}
	r.MaxObservations = int(count)
	budget, err := unsigned(f["max_bytes"])
	if err != nil || budget < 1 || budget > 256<<20 {
		return r, jsondocument.ErrInvalid
	}
	r.MaxBytes = int64(budget)
	return r, nil
}

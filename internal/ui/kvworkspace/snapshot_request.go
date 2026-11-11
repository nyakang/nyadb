package kvworkspace

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

const SnapshotMethod = "nyadb://etcd/snapshot"

func DefaultSnapshotRequest() contract.KVRequest {
	return contract.KVRequest{Method: SnapshotMethod, JSON: "{\n  \"body_bytes\": \"67108864\",\n  \"journal_bytes\": \"134217728\",\n  \"frames\": \"32768\"\n}"}
}
func snapshotRequestValues(r contract.KVRequest) (contract.KVSnapshotRequest, error) {
	var values contract.KVSnapshotRequest
	var fields map[string]json.RawMessage
	if r.Method != SnapshotMethod || jsondocument.Decode([]byte(r.JSON), &fields, inputLimit) != nil || len(fields) != 3 {
		return values, jsondocument.ErrInvalid
	}
	for key, target := range map[string]*uint64{"body_bytes": &values.BodyBytes, "journal_bytes": &values.JournalBytes, "frames": &values.Frames} {
		raw, ok := fields[key]
		if !ok {
			return values, jsondocument.ErrInvalid
		}
		var text string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &text) != nil || len(text) == 0 || len(text) > 19 {
			return values, jsondocument.ErrInvalid
		}
		for _, c := range text {
			if c < '0' || c > '9' {
				return values, jsondocument.ErrInvalid
			}
		}
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil || n == 0 || n > math.MaxInt64 {
			return values, jsondocument.ErrBounds
		}
		*target = n
	}
	if values.BodyBytes > math.MaxInt64-32 || values.JournalBytes > math.MaxInt64-32-values.BodyBytes || values.Frames < 2 {
		return values, jsondocument.ErrBounds
	}
	return values, nil
}
func (s *State) SetSnapshotInputs(d *Document, body, journal, frames string) {
	if !s.owned(d) || d.Busy() || d.method != SnapshotMethod {
		return
	}
	raw, _ := json.MarshalIndent(struct {
		Body    string `json:"body_bytes"`
		Journal string `json:"journal_bytes"`
		Frames  string `json:"frames"`
	}{body, journal, frames}, "", "  ")
	d.Editor.Buffer.Set(string(raw))
	d.snapshotState().confirmation = nil
	d.permit = nil
}

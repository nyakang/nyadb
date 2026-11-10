package kvworkspace

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// AlarmsMethod stores authored filters and exact selections only. Request files
// never contain an observation handle or permission to execute a Disarm.
const AlarmsMethod = "nyadb://etcd/alarms"
const alarmNativeMethod = "/etcdserverpb.Maintenance/Alarm"

type alarmTargetText struct {
	Member string `json:"member_id"`
	Alarm  string `json:"alarm"`
}
type alarmInputs struct {
	Alarm   string            `json:"alarm"`
	Targets []alarmTargetText `json:"targets"`
}

func DefaultAlarmsRequest() contract.KVRequest {
	return contract.KVRequest{Method: AlarmsMethod, JSON: "{\n  \"alarm\": \"NONE\",\n  \"targets\": []\n}"}
}

func alarmTypeName(kind int32) string {
	switch kind {
	case 0:
		return "NONE"
	case 1:
		return "NOSPACE"
	case 2:
		return "CORRUPT"
	default:
		return strconv.FormatInt(int64(kind), 10)
	}
}
func alarmTypeValue(name string) (int32, bool) {
	switch name {
	case "NONE":
		return 0, true
	case "NOSPACE":
		return 1, true
	case "CORRUPT":
		return 2, true
	default:
		return 0, false
	}
}

func parseAlarmInputs(r contract.KVRequest) (int32, []contract.KVAlarm, error) {
	var fields map[string]json.RawMessage
	if r.Method != AlarmsMethod || jsondocument.Decode([]byte(r.JSON), &fields, inputLimit) != nil || len(fields) != 2 || fields["alarm"] == nil || fields["targets"] == nil || string(fields["targets"]) == "null" {
		return 0, nil, jsondocument.ErrInvalid
	}
	var filter string
	if json.Unmarshal(fields["alarm"], &filter) != nil {
		return 0, nil, jsondocument.ErrInvalid
	}
	kind, ok := alarmTypeValue(filter)
	if !ok {
		return 0, nil, jsondocument.ErrInvalid
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(fields["targets"], &entries) != nil || len(entries) > 8 {
		return 0, nil, jsondocument.ErrInvalid
	}
	seen := map[[2]uint64]bool{}
	out := make([]contract.KVAlarm, 0, len(entries))
	for _, entry := range entries {
		var id, name string
		if len(entry) != 2 || json.Unmarshal(entry["member_id"], &id) != nil || json.Unmarshal(entry["alarm"], &name) != nil || len(id) == 0 || len(id) > 20 || strings.Trim(id, "0123456789") != "" {
			return 0, nil, jsondocument.ErrInvalid
		}
		member, err := strconv.ParseUint(id, 10, 64)
		typeID, known := alarmTypeValue(name)
		pair := [2]uint64{member, uint64(typeID)}
		if err != nil || member == 0 || !known || typeID == 0 || kind != 0 && kind != typeID || seen[pair] {
			return 0, nil, jsondocument.ErrInvalid
		}
		seen[pair] = true
		out = append(out, contract.KVAlarm{MemberID: member, Type: typeID})
	}
	return kind, out, nil
}

func alarmReadRequest(kind int32) contract.KVRequest {
	raw, _ := json.Marshal(struct {
		Action string `json:"action"`
		Alarm  string `json:"alarm"`
	}{"GET", alarmTypeName(kind)})
	return contract.KVRequest{Method: alarmNativeMethod, JSON: string(raw)}
}
func alarmDisarmRequest(alarm contract.KVAlarm) contract.KVRequest {
	raw, _ := json.Marshal(struct {
		Action string `json:"action"`
		Member string `json:"memberID"`
		Alarm  string `json:"alarm"`
	}{"DEACTIVATE", strconv.FormatUint(alarm.MemberID, 10), alarmTypeName(alarm.Type)})
	return contract.KVRequest{Method: alarmNativeMethod, JSON: string(raw)}
}

// Canonical protobuf JSON may omit the GET/NONE/zero defaults. Validate its
// semantics without depending on whitespace or the spelling of those defaults.
func sameAlarmNativeRequest(actual, expected contract.KVRequest) bool {
	decode := func(r contract.KVRequest) (string, uint64, int32, bool) {
		var fields map[string]json.RawMessage
		if r.Method != alarmNativeMethod || jsondocument.Decode([]byte(r.JSON), &fields, inputLimit) != nil || fields == nil || len(fields) > 3 {
			return "", 0, 0, false
		}
		action, member, kind := "GET", uint64(0), int32(0)
		memberSeen := false
		for key, raw := range fields {
			var text string
			switch key {
			case "action":
				if json.Unmarshal(raw, &text) != nil || text != "GET" && text != "DEACTIVATE" {
					return "", 0, 0, false
				}
				action = text
			case "alarm":
				if json.Unmarshal(raw, &text) != nil {
					return "", 0, 0, false
				}
				var ok bool
				kind, ok = alarmTypeValue(text)
				if !ok {
					return "", 0, 0, false
				}
			case "member_id", "memberID":
				if memberSeen {
					return "", 0, 0, false
				}
				memberSeen = true
				if json.Unmarshal(raw, &text) != nil || len(text) == 0 || len(text) > 20 || strings.Trim(text, "0123456789") != "" {
					return "", 0, 0, false
				}
				var err error
				member, err = strconv.ParseUint(text, 10, 64)
				if err != nil {
					return "", 0, 0, false
				}
			default:
				return "", 0, 0, false
			}
		}
		if action == "GET" && member != 0 || action == "DEACTIVATE" && (member == 0 || kind == 0) {
			return "", 0, 0, false
		}
		return action, member, kind, true
	}
	aa, am, ak, valid := decode(actual)
	ea, em, ek, expectedValid := decode(expected)
	return valid && expectedValid && aa == ea && am == em && ak == ek
}

func (s *State) SetAlarmSelection(d *Document, filter int32, targets []contract.KVAlarm) {
	if !s.owned(d) || d.Busy() || d.method != AlarmsMethod {
		return
	}
	v := alarmInputs{Alarm: alarmTypeName(filter), Targets: make([]alarmTargetText, 0, len(targets))}
	for _, target := range targets {
		v.Targets = append(v.Targets, alarmTargetText{strconv.FormatUint(target.MemberID, 10), alarmTypeName(target.Type)})
	}
	raw, _ := json.MarshalIndent(v, "", "  ")
	r := contract.KVRequest{Method: AlarmsMethod, JSON: string(raw)}
	if _, _, err := parseAlarmInputs(r); err != nil {
		d.error = err.Error()
		return
	}
	oldFilter, _, err := parseAlarmInputs(d.Request())
	if err != nil || oldFilter != filter {
		d.releaseAlarmAuthority()
	}
	d.Editor.Buffer.Set(r.JSON)
	d.confirmation, d.permit = nil, nil
}

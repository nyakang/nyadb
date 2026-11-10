package etcd

import (
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

// GET filters by alarm type only: official 3.5/3.7 ignore its member_id field.
// DEACTIVATE uses one explicit pair. No ACTIVATE or implicit disarm-all request
// is exposed, and no client helper may synthesize an acknowledgement.
func validateAlarmRequest(r *pb.AlarmRequest) (string, bool, error) {
	if r.Alarm < pb.AlarmType_NONE || r.Alarm > pb.AlarmType_CORRUPT {
		return "", false, ErrInvalid
	}
	switch r.Action {
	case pb.AlarmRequest_GET:
		if r.MemberID != 0 {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.Maintenance/Alarm", false, nil
	case pb.AlarmRequest_DEACTIVATE:
		if r.MemberID == 0 || r.Alarm == pb.AlarmType_NONE {
			return "", false, ErrInvalid
		}
		return "/etcdserverpb.Maintenance/Alarm", true, nil
	default:
		return "", false, ErrInvalid
	}
}

func (c *Client) validateAlarmResponse(request, reply proto.Message) error {
	r, ok := request.(*pb.AlarmRequest)
	if !ok {
		return nil
	}
	v, ok := reply.(*pb.AlarmResponse)
	if !ok || !c.matches(v.Header) {
		return ErrProtocol
	}
	seen := make(map[[2]uint64]bool, len(v.Alarms))
	for _, alarm := range v.Alarms {
		if alarm == nil || alarm.MemberID == 0 || alarm.Alarm == pb.AlarmType_NONE {
			return ErrProtocol
		}
		key := [2]uint64{alarm.MemberID, uint64(uint32(alarm.Alarm))}
		if seen[key] {
			return ErrProtocol
		}
		seen[key] = true
		if r.Action == pb.AlarmRequest_DEACTIVATE && (alarm.MemberID != r.MemberID || alarm.Alarm != r.Alarm) || r.Action == pb.AlarmRequest_GET && r.Alarm != pb.AlarmType_NONE && alarm.Alarm != r.Alarm {
			return ErrProtocol
		}
	}
	return nil
}

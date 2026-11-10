package etcd

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Records intentionally omit password material from native Auth requests.
// They retain exact non-secret parameters and explicit omission metadata; the
// captured preparation and its original executor still own the complete secret.
func recordedAuthRequest(request contract.KVRequest, native proto.Message) contract.KVRequestRecord {
	r := contract.KVRequestRecord{Request: request}
	var redacted proto.Message
	switch v := native.(type) {
	case *pb.AuthUserAddRequest:
		c := proto.Clone(v).(*pb.AuthUserAddRequest)
		if c.Password != "" {
			r.OmittedFields = append(r.OmittedFields, "password")
		}
		if c.HashedPassword != "" {
			r.OmittedFields = append(r.OmittedFields, "hashedPassword")
		}
		c.Password, c.HashedPassword = "", ""
		redacted = c
	case *pb.AuthUserChangePasswordRequest:
		c := proto.Clone(v).(*pb.AuthUserChangePasswordRequest)
		if c.Password != "" {
			r.OmittedFields = append(r.OmittedFields, "password")
		}
		if c.HashedPassword != "" {
			r.OmittedFields = append(r.OmittedFields, "hashedPassword")
		}
		c.Password, c.HashedPassword = "", ""
		redacted = c
	}
	if len(r.OmittedFields) == 0 {
		return r
	}
	// Every accepted preparation was already validated and serialized; clearing
	// strings cannot introduce a serialization failure or exceed its budget.
	b, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(redacted)
	if err != nil {
		r.Request.JSON = ""
	} else {
		r.Request.JSON = string(b)
	}
	return r
}

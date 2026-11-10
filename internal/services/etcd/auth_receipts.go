package etcd

import (
	"slices"

	"github.com/nyakang/nyadb/internal/db/contract"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func validAuthNames(names []string) bool {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !validAuthName(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func validateAuthReply(request, native proto.Message) error {
	if authResponse(request) == nil {
		return nil
	}
	if native == nil || native.ProtoReflect().Descriptor().FullName() != authResponse(request).ProtoReflect().Descriptor().FullName() {
		return ErrProtocol
	}
	switch r := native.(type) {
	case *pb.AuthUserListResponse:
		if !validAuthNames(r.Users) {
			return ErrProtocol
		}
	case *pb.AuthUserGetResponse:
		if !validAuthNames(r.Roles) {
			return ErrProtocol
		}
	case *pb.AuthRoleListResponse:
		if !validAuthNames(r.Roles) {
			return ErrProtocol
		}
	case *pb.AuthRoleGetResponse:
		seen := map[struct{ key, end string }]bool{}
		for _, permission := range r.Perm {
			if permission == nil || permission.PermType < 0 || permission.PermType > 2 || !validKey(permission.Key) || !validEnd(permission.Key, permission.RangeEnd) {
				return ErrProtocol
			}
			identity := struct{ key, end string }{string(permission.Key), string(permission.RangeEnd)}
			if seen[identity] {
				return ErrProtocol
			}
			seen[identity] = true
		}
	}
	return nil
}

func projectAuthReply(native proto.Message, budget *int) (contract.KVAuthReceipt, string, error) {
	var r contract.KVAuthReceipt
	kind := ""
	switch v := native.(type) {
	case *pb.AuthStatusResponse:
		kind = "auth_status"
		r.Enabled, r.Revision = v.Enabled, v.AuthRevision
	case *pb.AuthUserListResponse:
		kind = "auth_user_list"
		r.Users = slices.Clone(v.Users)
	case *pb.AuthUserGetResponse:
		kind = "auth_user_get"
		r.Roles = slices.Clone(v.Roles)
	case *pb.AuthRoleListResponse:
		kind = "auth_role_list"
		r.Roles = slices.Clone(v.Roles)
	case *pb.AuthRoleGetResponse:
		kind = "auth_role_get"
		*budget -= len(v.Perm)
		if *budget < 0 {
			return r, kind, ErrBounds
		}
		r.Permissions = make([]contract.KVAuthPermission, len(v.Perm))
		for i, p := range v.Perm {
			if p == nil {
				return r, kind, ErrProtocol
			}
			r.Permissions[i] = contract.KVAuthPermission{Access: p.PermType.String(), Key: slices.Clone(p.Key), End: slices.Clone(p.RangeEnd), NativeExtensions: slices.Clone(p.ProtoReflect().GetUnknown())}
		}
	case *pb.AuthUserAddResponse:
		kind = "auth_user_add"
	case *pb.AuthUserDeleteResponse:
		kind = "auth_user_delete"
	case *pb.AuthUserChangePasswordResponse:
		kind = "auth_user_change_password"
	case *pb.AuthUserGrantRoleResponse:
		kind = "auth_user_grant_role"
	case *pb.AuthUserRevokeRoleResponse:
		kind = "auth_user_revoke_role"
	case *pb.AuthRoleAddResponse:
		kind = "auth_role_add"
	case *pb.AuthRoleDeleteResponse:
		kind = "auth_role_delete"
	case *pb.AuthRoleGrantPermissionResponse:
		kind = "auth_role_grant_permission"
	case *pb.AuthRoleRevokePermissionResponse:
		kind = "auth_role_revoke_permission"
	default:
		return r, kind, ErrProtocol
	}
	*budget -= len(r.Users) + len(r.Roles)
	if *budget < 0 {
		return r, kind, ErrBounds
	}
	return r, kind, nil
}

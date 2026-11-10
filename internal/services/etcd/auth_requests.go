package etcd

import (
	"unicode/utf8"

	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

func validAuthName(name string) bool {
	return name != "" && utf8.ValidString(name) && len(name) <= requestLimit
}

func validAuthPermission(p *authpb.Permission) bool {
	return p != nil && len(p.ProtoReflect().GetUnknown()) == 0 && p.PermType >= authpb.READ && p.PermType <= authpb.READWRITE && validKey(p.Key) && validEnd(p.Key, p.RangeEnd)
}

func validAuthPassword(password, hashed string, noPassword bool) bool {
	if !utf8.ValidString(password) || !utf8.ValidString(hashed) || len(password)+len(hashed) > requestLimit {
		return false
	}
	if noPassword {
		return password == "" && hashed == ""
	}
	return password != "" && hashed == "" || password == "" && hashed != ""
}

func authRequest(method string) proto.Message {
	switch method {
	case "/etcdserverpb.Auth/AuthStatus":
		return &pb.AuthStatusRequest{}
	case "/etcdserverpb.Auth/UserList":
		return &pb.AuthUserListRequest{}
	case "/etcdserverpb.Auth/UserGet":
		return &pb.AuthUserGetRequest{}
	case "/etcdserverpb.Auth/UserAdd":
		return &pb.AuthUserAddRequest{}
	case "/etcdserverpb.Auth/UserDelete":
		return &pb.AuthUserDeleteRequest{}
	case "/etcdserverpb.Auth/UserChangePassword":
		return &pb.AuthUserChangePasswordRequest{}
	case "/etcdserverpb.Auth/UserGrantRole":
		return &pb.AuthUserGrantRoleRequest{}
	case "/etcdserverpb.Auth/UserRevokeRole":
		return &pb.AuthUserRevokeRoleRequest{}
	case "/etcdserverpb.Auth/RoleList":
		return &pb.AuthRoleListRequest{}
	case "/etcdserverpb.Auth/RoleGet":
		return &pb.AuthRoleGetRequest{}
	case "/etcdserverpb.Auth/RoleAdd":
		return &pb.AuthRoleAddRequest{}
	case "/etcdserverpb.Auth/RoleDelete":
		return &pb.AuthRoleDeleteRequest{}
	case "/etcdserverpb.Auth/RoleGrantPermission":
		return &pb.AuthRoleGrantPermissionRequest{}
	case "/etcdserverpb.Auth/RoleRevokePermission":
		return &pb.AuthRoleRevokePermissionRequest{}
	}
	return nil
}

func validateAuthRequest(request proto.Message) (string, bool, error) {
	name, write, valid := "", false, false
	switch r := request.(type) {
	case *pb.AuthStatusRequest:
		name, valid = "AuthStatus", true
	case *pb.AuthUserListRequest:
		name, valid = "UserList", true
	case *pb.AuthUserGetRequest:
		name, valid = "UserGet", validAuthName(r.Name)
	case *pb.AuthRoleListRequest:
		name, valid = "RoleList", true
	case *pb.AuthRoleGetRequest:
		name, valid = "RoleGet", validAuthName(r.Role)
	case *pb.AuthUserAddRequest:
		name, write = "UserAdd", true
		valid = validAuthName(r.Name) && (r.Options == nil || len(r.Options.ProtoReflect().GetUnknown()) == 0) && validAuthPassword(r.Password, r.HashedPassword, r.Options.GetNoPassword())
	case *pb.AuthUserChangePasswordRequest:
		name, write, valid = "UserChangePassword", true, validAuthName(r.Name) && validAuthPassword(r.Password, r.HashedPassword, false)
	case *pb.AuthUserDeleteRequest:
		name, write, valid = "UserDelete", true, validAuthName(r.Name)
	case *pb.AuthUserGrantRoleRequest:
		name, write, valid = "UserGrantRole", true, validAuthName(r.User) && validAuthName(r.Role)
	case *pb.AuthUserRevokeRoleRequest:
		name, write, valid = "UserRevokeRole", true, validAuthName(r.Name) && validAuthName(r.Role)
	case *pb.AuthRoleAddRequest:
		name, write, valid = "RoleAdd", true, validAuthName(r.Name)
	case *pb.AuthRoleDeleteRequest:
		name, write, valid = "RoleDelete", true, validAuthName(r.Role)
	case *pb.AuthRoleGrantPermissionRequest:
		name, write, valid = "RoleGrantPermission", true, validAuthName(r.Name) && validAuthPermission(r.Perm)
	case *pb.AuthRoleRevokePermissionRequest:
		name, write, valid = "RoleRevokePermission", true, validAuthName(r.Role) && validKey(r.Key) && validEnd(r.Key, r.RangeEnd)
	}
	if !valid {
		return "", false, ErrInvalid
	}
	return "/etcdserverpb.Auth/" + name, write, nil
}

func authResponse(request proto.Message) proto.Message {
	switch request.(type) {
	case *pb.AuthStatusRequest:
		return &pb.AuthStatusResponse{}
	case *pb.AuthUserListRequest:
		return &pb.AuthUserListResponse{}
	case *pb.AuthUserGetRequest:
		return &pb.AuthUserGetResponse{}
	case *pb.AuthUserAddRequest:
		return &pb.AuthUserAddResponse{}
	case *pb.AuthUserDeleteRequest:
		return &pb.AuthUserDeleteResponse{}
	case *pb.AuthUserChangePasswordRequest:
		return &pb.AuthUserChangePasswordResponse{}
	case *pb.AuthUserGrantRoleRequest:
		return &pb.AuthUserGrantRoleResponse{}
	case *pb.AuthUserRevokeRoleRequest:
		return &pb.AuthUserRevokeRoleResponse{}
	case *pb.AuthRoleListRequest:
		return &pb.AuthRoleListResponse{}
	case *pb.AuthRoleGetRequest:
		return &pb.AuthRoleGetResponse{}
	case *pb.AuthRoleAddRequest:
		return &pb.AuthRoleAddResponse{}
	case *pb.AuthRoleDeleteRequest:
		return &pb.AuthRoleDeleteResponse{}
	case *pb.AuthRoleGrantPermissionRequest:
		return &pb.AuthRoleGrantPermissionResponse{}
	case *pb.AuthRoleRevokePermissionRequest:
		return &pb.AuthRoleRevokePermissionResponse{}
	}
	return nil
}

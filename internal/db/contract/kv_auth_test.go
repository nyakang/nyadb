package contract

import "testing"

func TestKVAuthSnapshotOwnsNamesBinaryPermissionsAndOmissionMetadata(t *testing.T) {
	r := KVResult{RequestOmittedFields: []string{"password"}, Reply: &KVReply{Auth: &KVAuthReceipt{Users: []string{" original user "}, Roles: []string{" original role "}, Permissions: []KVAuthPermission{{Access: "READ", Key: []byte{0, 255}, End: []byte{0}, NativeExtensions: []byte{7}}}}}}
	c := r.Clone()
	c.RequestOmittedFields[0] = "changed"
	c.Reply.Auth.Users[0] = "changed"
	c.Reply.Auth.Roles[0] = "changed"
	c.Reply.Auth.Permissions[0].Key[0] = 1
	c.Reply.Auth.Permissions[0].End[0] = 1
	c.Reply.Auth.Permissions[0].NativeExtensions[0] = 1
	if r.RequestOmittedFields[0] != "password" || r.Reply.Auth.Users[0] != " original user " || r.Reply.Auth.Roles[0] != " original role " || r.Reply.Auth.Permissions[0].Key[0] != 0 || r.Reply.Auth.Permissions[0].End[0] != 0 || r.Reply.Auth.Permissions[0].NativeExtensions[0] != 7 {
		t.Fatal("Auth projection/omission clone aliases original")
	}
}

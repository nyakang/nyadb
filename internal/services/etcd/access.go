package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"unicode/utf8"

	authpb "go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var errAccessChanged = errors.New("original etcd permissions changed; explicitly reopen the data document")

// Ranges use mathematical half-open byte endpoints. End={0} means infinity.
// An exact key ends at key+NUL, including at 4 KiB. RPC construction restores
// exact-key requests without confusing a maximum-key prefix with a singleton.
type byteRange struct{ Start, End []byte }

func infinite(end []byte) bool { return bytes.Equal(end, []byte{0}) }
func compareEnds(a, b []byte) int {
	if infinite(a) {
		if infinite(b) {
			return 0
		}
		return 1
	}
	if infinite(b) {
		return -1
	}
	return bytes.Compare(a, b)
}
func exactRange(key []byte) byteRange {
	return byteRange{bytes.Clone(key), AfterKey(key)}
}
func (r byteRange) contains(key []byte) bool {
	return bytes.Compare(key, r.Start) >= 0 && (infinite(r.End) || bytes.Compare(key, r.End) < 0)
}
func (r byteRange) request(start []byte, limit, revision int64) *pb.RangeRequest {
	out := &pb.RangeRequest{Key: bytes.Clone(start), RangeEnd: bytes.Clone(r.End), Limit: limit, Revision: revision, SortOrder: pb.RangeRequest_ASCEND, SortTarget: pb.RangeRequest_KEY}
	if compareEnds(exactRange(start).End, r.End) == 0 {
		out.RangeEnd = nil
	}
	return out
}
func normalizeRanges(input []byteRange) []byteRange {
	ranges := make([]byteRange, len(input))
	for i, r := range input {
		ranges[i] = byteRange{bytes.Clone(r.Start), bytes.Clone(r.End)}
	}
	sort.Slice(ranges, func(i, j int) bool {
		if d := bytes.Compare(ranges[i].Start, ranges[j].Start); d != 0 {
			return d < 0
		}
		return compareEnds(ranges[i].End, ranges[j].End) < 0
	})
	var out []byteRange
	for _, r := range ranges {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if infinite(last.End) || bytes.Compare(r.Start, last.End) <= 0 {
				end := last.End
				if compareEnds(r.End, end) > 0 {
					end = r.End
				}
				// A 4 KiB singleton's exclusive successor is 4097 bytes.
				// Keep it separate when merging would require an unsupported
				// larger RangeEnd. Exact RPCs remain valid and complete.
				if len(end) > 4096 && !bytes.Equal(last.Start, end[:len(end)-1]) {
					out = append(out, r)
					continue
				}
				if compareEnds(r.End, last.End) > 0 {
					last.End = bytes.Clone(r.End)
				}
				continue
			}
		}
		out = append(out, r)
	}
	return out
}
func intersectRanges(a, b []byteRange) []byteRange {
	left, right := normalizeRanges(a), normalizeRanges(b)
	var out []byteRange
	for i, j := 0, 0; i < len(left) && j < len(right); {
		start := left[i].Start
		if bytes.Compare(right[j].Start, start) > 0 {
			start = right[j].Start
		}
		end := left[i].End
		if compareEnds(right[j].End, end) < 0 {
			end = right[j].End
		}
		if infinite(end) || bytes.Compare(start, end) < 0 {
			out = append(out, byteRange{bytes.Clone(start), bytes.Clone(end)})
		}
		d := compareEnds(left[i].End, right[j].End)
		if d <= 0 {
			i++
		}
		if d >= 0 {
			j++
		}
	}
	return out
}
func prefixRanges(prefix []byte) []byteRange {
	r := PrefixRange(prefix, 1, 0)
	return []byteRange{{r.Key, r.RangeEnd}}
}
func allowsKey(ranges []byteRange, key []byte) bool {
	for _, r := range ranges {
		if r.contains(key) {
			return true
		}
	}
	return false
}
func permissionRange(p *authpb.Permission) (byteRange, error) {
	if p == nil || p.PermType < authpb.READ || p.PermType > authpb.READWRITE || len(p.ProtoReflect().GetUnknown()) > 0 {
		return byteRange{}, ErrProtocol
	}
	if len(p.RangeEnd) == 0 {
		if !validKey(p.Key) {
			return byteRange{}, ErrProtocol
		}
		return exactRange(p.Key), nil
	}
	start := p.Key
	if len(start) == 0 {
		start = []byte{0}
	}
	if !validKey(start) || !validEnd(start, p.RangeEnd) {
		return byteRange{}, ErrProtocol
	}
	return byteRange{bytes.Clone(start), bytes.Clone(p.RangeEnd)}, nil
}

type keyAccess struct {
	Principal string
	Enabled   bool
	// etcd 3.5 denies AuthStatus to non-admins. Zero denotes an unavailable
	// auth revision; the captured grants remain immutable and server-enforced.
	AuthRevision     uint64
	Read, Write      []byteRange
	ObservedRevision int64 `json:"-"`
}

func (a *keyAccess) clone() *keyAccess {
	if a == nil {
		return nil
	}
	out := *a
	out.Read = normalizeRanges(a.Read)
	out.Write = normalizeRanges(a.Write)
	return &out
}
func (a *keyAccess) fingerprint() string {
	raw, _ := json.Marshal(a)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (a *keyAccess) scoped(prefix []byte) *keyAccess {
	out := a.clone()
	out.Read = intersectRanges(out.Read, prefixRanges(prefix))
	out.Write = intersectRanges(out.Write, out.Read)
	return out
}

func (c *Client) permissions(parent context.Context) (a *keyAccess, err error) {
	return c.capturePermissions(parent, false)
}

// Fresh observations authorize an explicit key action without replacing the
// data document's immutable original grant geometry.
func (c *Client) capturePermissions(parent context.Context, fresh bool) (a *keyAccess, err error) {
	if err = parent.Err(); err != nil {
		return nil, err
	}
	if !c.operation.TryLock() {
		return nil, ErrBusy
	}
	defer c.operation.Unlock()
	if !c.Alive() {
		return nil, ErrRetired
	}
	c.mu.Lock()
	cached := c.access.clone()
	c.mu.Unlock()
	if cached != nil && !fresh {
		return cached, nil
	}
	ctx, cancel := context.WithTimeout(parent, c.config.RequestTimeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer func() {
		if err != nil && (ctx.Err() != nil || status.Code(err) == codes.Unavailable || status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.ResourceExhausted || errors.Is(err, ErrProtocol)) {
			c.mu.Lock()
			c.retired = true
			c.mu.Unlock()
			c.closeConnection()
		}
	}()
	auth := pb.NewAuthClient(c.conn)
	before, e := auth.AuthStatus(c.authContext(ctx), &pb.AuthStatusRequest{})
	known := e == nil
	if e != nil && status.Code(e) != codes.PermissionDenied {
		return nil, e
	}
	a = &keyAccess{Principal: c.principal, Enabled: true, ObservedRevision: c.identity.Revision}
	if known {
		if before == nil || !c.matches(before.Header) {
			return nil, ErrProtocol
		}
		a.Enabled, a.AuthRevision, a.ObservedRevision = before.Enabled, before.AuthRevision, before.Header.Revision
	}
	if !a.Enabled {
		all := prefixRanges(nil)
		a.Read, a.Write = all, normalizeRanges(all)
	} else {
		if a.Principal == "" {
			return nil, status.Error(codes.PermissionDenied, "authenticated key identity is unavailable")
		}
		user, e := auth.UserGet(c.authContext(ctx), &pb.AuthUserGetRequest{Name: a.Principal})
		if e != nil {
			return nil, e
		}
		if user == nil || !c.matches(user.Header) || len(user.Roles) > 256 {
			return nil, ErrProtocol
		}
		a.ObservedRevision = max(a.ObservedRevision, user.Header.Revision)
		roles := slices.Clone(user.Roles)
		sort.Strings(roles)
		roles = slices.Compact(roles)
		if slices.Contains(roles, "root") {
			all := prefixRanges(nil)
			a.Read, a.Write = all, normalizeRanges(all)
		} else {
			total, size := 0, 0
			captured := make(map[string]*pb.AuthRoleGetResponse, len(roles))
			for _, role := range roles {
				if role == "" || len(role) > 4096 || !utf8.ValidString(role) {
					return nil, ErrProtocol
				}
				reply, e := auth.RoleGet(c.authContext(ctx), &pb.AuthRoleGetRequest{Role: role})
				if e != nil {
					return nil, e
				}
				if reply == nil || !c.matches(reply.Header) {
					return nil, ErrProtocol
				}
				total += len(reply.Perm)
				size += proto.Size(reply)
				if total > 4096 || size > 8<<20 {
					return nil, ErrBounds
				}
				if !known {
					captured[role] = reply
				}
				a.ObservedRevision = max(a.ObservedRevision, reply.Header.Revision)
				for _, p := range reply.Perm {
					r, e := permissionRange(p)
					if e != nil {
						return nil, e
					}
					if p.PermType != authpb.WRITE {
						a.Read = append(a.Read, r)
					}
					if p.PermType != authpb.READ {
						a.Write = append(a.Write, r)
					}
				}
			}
			// 3.5 does not expose the auth revision to restricted identities.
			// Read each role again and reject a changed permission set during
			// capture. This does not claim an atomic auth snapshot on 3.5;
			// the original geometry stays immutable and every KV RPC is still
			// authorized by the server at execution time.
			for _, role := range roles {
				if known {
					break
				}
				reply, e := auth.RoleGet(c.authContext(ctx), &pb.AuthRoleGetRequest{Role: role})
				if e != nil {
					return nil, e
				}
				if reply == nil || !c.matches(reply.Header) || proto.Size(reply) > 8<<20 {
					return nil, ErrProtocol
				}
				if !permissionSetsEqual(captured[role].Perm, reply.Perm) {
					return nil, errAccessChanged
				}
			}
		}
		// Self-role membership is readable on both supported versions. Detect
		// changes during capture without inventing an auth revision on etcd 3.5.
		afterUser, e := auth.UserGet(c.authContext(ctx), &pb.AuthUserGetRequest{Name: a.Principal})
		if e != nil {
			return nil, e
		}
		if afterUser == nil || !c.matches(afterUser.Header) || len(afterUser.Roles) > 256 {
			return nil, ErrProtocol
		}
		afterRoles := slices.Clone(afterUser.Roles)
		sort.Strings(afterRoles)
		afterRoles = slices.Compact(afterRoles)
		if !slices.Equal(roles, afterRoles) {
			return nil, errAccessChanged
		}
	}
	if known {
		after, e := auth.AuthStatus(c.authContext(ctx), &pb.AuthStatusRequest{})
		if e != nil {
			return nil, e
		}
		if after == nil || !c.matches(after.Header) {
			return nil, ErrProtocol
		}
		if after.Enabled != a.Enabled || after.AuthRevision != a.AuthRevision {
			return nil, errAccessChanged
		}
	}
	a.Read, a.Write = normalizeRanges(a.Read), normalizeRanges(a.Write)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.access = a.clone()
	c.mu.Unlock()
	return a, nil
}

func permissionSetsEqual(a, b []*authpb.Permission) bool {
	if len(a) != len(b) {
		return false
	}
	// etcd returns sorted permissions; compare the actual wire observations
	// conservatively rather than treating an unobserved grant as equivalent.
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

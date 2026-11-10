package etcd

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"

	authpb "go.etcd.io/etcd/api/v3/authpb"
)

func TestAccessBinaryRangesUnionIntersectionAndOriginalOwnership(t *testing.T) {
	input := []byteRange{{[]byte{3}, []byte{8}}, {[]byte{1}, []byte{4}}, {[]byte{8}, []byte{9}}, exactRange([]byte{0, 255}), {[]byte{255}, []byte{0}}}
	out := normalizeRanges(input)
	want := []byteRange{exactRange([]byte{0, 255}), {[]byte{1}, []byte{9}}, {[]byte{255}, []byte{0}}}
	if !reflect.DeepEqual(out, want) {
		t.Fatal("byte union", out)
	}
	input[0].Start[0] = 99
	if !bytes.Equal(out[1].Start, []byte{1}) {
		t.Fatal("normalization aliases source")
	}
	prefix := []byte{255, 255}
	scoped := intersectRanges(out, prefixRanges(prefix))
	if len(scoped) != 1 || !bytes.Equal(scoped[0].Start, prefix) || !infinite(scoped[0].End) || allowsKey(scoped, []byte{255, 254}) || !allowsKey(scoped, bytes.Repeat([]byte{255}, 4096)) {
		t.Fatal("all-ff sentinel intersection")
	}
}
func TestAccessMaximumExactKeyUsesNativeExactRange(t *testing.T) {
	for _, key := range [][]byte{[]byte("key"), bytes.Repeat([]byte{255}, 4096), append(bytes.Repeat([]byte{255}, 4095), 254)} {
		r, err := permissionRange(&authpb.Permission{Key: key, PermType: authpb.READ})
		if err != nil || !r.contains(key) || len(r.request(key, 1, 4).RangeEnd) != 0 {
			t.Fatal("exact key expanded", err)
		}
		next := dataAfterKey(key)
		if next != nil && r.contains(next) {
			t.Fatal("exact key includes a successor")
		}
	}
	for _, p := range []*authpb.Permission{nil, {Key: nil}, {Key: []byte("z"), RangeEnd: []byte("a")}, {Key: []byte("x"), PermType: 77}} {
		if _, err := permissionRange(p); err == nil {
			t.Fatal("invalid native permission accepted")
		}
	}
}

func TestAccessMaximumSingletonUnionPreservesNativePermissionMembership(t *testing.T) {
	maximum := bytes.Repeat([]byte{255}, 4096)
	before := bytes.Clone(maximum)
	before[len(before)-1]--
	input := []byteRange{{[]byte("a"), maximum}, exactRange(maximum)}
	output := normalizeRanges(input)
	keys := [][]byte{[]byte("a"), []byte("b"), before, AfterKey(before), maximum, AfterKey(maximum)}
	for _, r := range output {
		if _, _, err := validateRequest(r.request(r.Start, 1, 1)); err != nil {
			t.Fatal("union requires an unsupported native endpoint", err)
		}
	}
	for _, key := range keys {
		wireAllowed := false
		for _, r := range output {
			request := r.request(r.Start, 1, 1)
			if len(request.RangeEnd) == 0 {
				wireAllowed = wireAllowed || bytes.Equal(request.Key, key)
			} else {
				wireAllowed = wireAllowed || (byteRange{request.Key, request.RangeEnd}).contains(key)
			}
		}
		if wireAllowed != allowsKey(input, key) {
			t.Fatal("union expanded or lost native permission membership", len(key))
		}
	}
	if allowsKey(output, AfterKey(maximum)) || len(prefixRanges(maximum)[0].request(maximum, 1, 1).RangeEnd) == 0 {
		t.Fatal("maximum singleton or prefix collapsed across the supported key boundary")
	}
}
func TestAccessRangeAlgebraMatchesFiniteByteMembership(t *testing.T) {
	rng := rand.New(rand.NewSource(33))
	for trial := 0; trial < 100; trial++ {
		var a, b []byteRange
		for _, dst := range []*[]byteRange{&a, &b} {
			for n := 0; n < 9; n++ {
				start := byte(rng.Intn(254) + 1)
				end := start + byte(rng.Intn(int(255-start))+1)
				*dst = append(*dst, byteRange{[]byte{start}, []byte{end}})
			}
		}
		normalized := normalizeRanges(a)
		intersection := intersectRanges(a, b)
		for key := 1; key < 255; key++ {
			k := []byte{byte(key)}
			if allowsKey(normalized, k) != allowsKey(a, k) || allowsKey(intersection, k) != (allowsKey(a, k) && allowsKey(b, k)) {
				t.Fatal("range algebra changed membership", trial, key)
			}
		}
	}
}
func TestAccessSnapshotsExcludeReadOnlyKeysAndDoNotAlias(t *testing.T) {
	all := prefixRanges(nil)
	a := &keyAccess{Principal: " exact user ", Enabled: true, AuthRevision: 10, Read: all, Write: []byteRange{exactRange([]byte("a"))}, ObservedRevision: 7}
	copy := a.scoped(nil)
	if !allowsKey(copy.Read, []byte("b")) || allowsKey(copy.Write, []byte("b")) || !allowsKey(copy.Write, []byte("a")) {
		t.Fatal("read-only permissions collapsed")
	}
	before := copy.fingerprint()
	a.Write[0].Start[0] = 'x'
	a.Principal = "changed"
	if copy.fingerprint() != before {
		t.Fatal("captured permissions alias source")
	}
	copy.ObservedRevision++
	if copy.fingerprint() != before {
		t.Fatal("ordinary key revision changed permission identity")
	}
	copy.AuthRevision++
	if copy.fingerprint() == before {
		t.Fatal("auth revision not captured")
	}
}

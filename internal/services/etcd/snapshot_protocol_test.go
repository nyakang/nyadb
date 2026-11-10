package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
)

func snapshotTestProof(t *testing.T, maxBody uint64) *snapshotProof {
	t.Helper()
	p, err := newSnapshotProof(Identity{Header: Header{ClusterID: 1, MemberID: 2}, Version: "3.7.3"}, maxBody, 20)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSnapshotProofRequiresOriginalChecksumAndEOFBeyondZeroRemaining(t *testing.T) {
	for _, version := range []string{"", "3.7.0"} {
		body := bytes.Repeat([]byte{0, 255, 37, 91}, 5120)
		p := snapshotTestProof(t, uint64(len(body)))
		if kind, err := p.accept(&pb.SnapshotResponse{Blob: body, Version: version}); err != nil || kind != snapshotData || p.matched || p.eof {
			t.Fatal("native zero remaining alone became complete", err)
		}
		checksum := sha256.Sum256(body)
		frame := &pb.SnapshotResponse{Blob: bytes.Clone(checksum[:]), Version: version}
		if kind, err := p.accept(frame); err != nil || kind != snapshotChecksum || p.eof || !p.matched {
			t.Fatal("independent checksum frame changed", err)
		}
		frame.Blob[0] ^= 255
		if !bytes.Equal(p.checksum, checksum[:]) || p.version != version {
			t.Fatal("original checksum aliases the caller or invented a product version")
		}
		if err := p.finish(io.EOF, context.Background()); err != nil || !p.eof || p.failure != nil || p.body != uint64(len(body)) {
			t.Fatal("complete native stream rejected", err)
		}
		if _, err := p.accept(&pb.SnapshotResponse{Blob: body}); !errors.Is(err, ErrUsed) {
			t.Fatal("ended original stream resumed")
		}
	}
}

func TestSnapshotProofChecksEachDataWindowAndRejectsChangedOrTrailingFrames(t *testing.T) {
	body := []byte("abcdefghijk")
	checksum := sha256.Sum256(body)
	p := snapshotTestProof(t, uint64(len(body)))
	for _, frame := range []*pb.SnapshotResponse{{Blob: body[:4], RemainingBytes: 7}, {Blob: body[4:9], RemainingBytes: 2}, {Blob: body[9:]}, {Blob: checksum[:]}} {
		if _, err := p.accept(frame); err != nil {
			t.Fatal("valid changing native window", err)
		}
	}
	if _, err := p.accept(&pb.SnapshotResponse{Blob: []byte("extra")}); !errors.Is(err, ErrProtocol) {
		t.Fatal("trailing native data accepted", err)
	}
	if err := p.finish(io.EOF, context.Background()); !errors.Is(err, ErrUsed) {
		t.Fatal("a failed stream was completed after trailing data")
	}
	for name, frame := range map[string]*pb.SnapshotResponse{
		"decreased":               {Blob: []byte("ef"), RemainingBytes: 1},
		"increased":               {Blob: []byte("ef"), RemainingBytes: 8},
		"empty":                   {},
		"foreign-cluster":         {Blob: []byte("efghijk"), Header: &pb.ResponseHeader{ClusterId: 9}},
		"foreign-member":          {Blob: []byte("efghijk"), Header: &pb.ResponseHeader{MemberId: 9}},
		"negative-revision":       {Blob: []byte("efghijk"), Header: &pb.ResponseHeader{Revision: -1}},
		"changed-storage-version": {Blob: []byte("efghijk"), Version: "3.7.1"},
	} {
		t.Run(name, func(t *testing.T) {
			p := snapshotTestProof(t, 32)
			_, _ = p.accept(&pb.SnapshotResponse{Blob: body[:4], RemainingBytes: 7, Version: "3.7.0"})
			if _, err := p.accept(frame); err == nil {
				t.Fatal("changed native window/header/version accepted")
			}
		})
	}
}

func TestSnapshotProofKeepsEarlyEOFHashErrorCancellationAndBoundsIncomplete(t *testing.T) {
	for _, phase := range []string{"before-data", "during-data", "missing-hash", "after-hash"} {
		p := snapshotTestProof(t, 32)
		body := []byte("ab")
		if phase != "before-data" {
			remaining := uint64(0)
			if phase == "during-data" {
				remaining = 2
			}
			_, _ = p.accept(&pb.SnapshotResponse{Blob: body, RemainingBytes: remaining})
		}
		ctx, cancel := context.WithCancel(context.Background())
		if phase == "after-hash" {
			checksum := sha256.Sum256(body)
			_, _ = p.accept(&pb.SnapshotResponse{Blob: checksum[:]})
			cancel()
		}
		if err := p.finish(io.EOF, ctx); err == nil || !p.eof || p.failure == nil {
			t.Fatal("incomplete/canceled original EOF became success", phase, err)
		}
		cancel()
	}
	for _, frame := range []*pb.SnapshotResponse{{Blob: []byte("ab"), RemainingBytes: math.MaxUint64}, {Blob: bytes.Repeat([]byte{'a'}, 33)}} {
		p := snapshotTestProof(t, 32)
		if _, err := p.accept(frame); !errors.Is(err, ErrBounds) {
			t.Fatal("overflow/oversize count accepted", err)
		}
	}
	for _, badHash := range [][]byte{nil, bytes.Repeat([]byte{1}, 31), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{1}, 33)} {
		p := snapshotTestProof(t, 32)
		_, _ = p.accept(&pb.SnapshotResponse{Blob: []byte("ab")})
		if _, err := p.accept(&pb.SnapshotResponse{Blob: badHash}); !errors.Is(err, ErrProtocol) {
			t.Fatal("missing/partial/wrong checksum accepted", err)
		}
	}
	p := snapshotTestProof(t, 32)
	p.maxFrames = 2
	_, _ = p.accept(&pb.SnapshotResponse{Blob: []byte("a"), RemainingBytes: 2})
	_, _ = p.accept(&pb.SnapshotResponse{Blob: []byte("b"), RemainingBytes: 1})
	if _, err := p.accept(&pb.SnapshotResponse{Blob: []byte("c")}); !errors.Is(err, ErrBounds) {
		t.Fatal("frame budget ignored", err)
	}
}

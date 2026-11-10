package etcd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"hash"
	"io"
	"math"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/protobuf/proto"
)

// These are independent stream/disk bounds. A frame's remaining count excludes
// the separate native checksum frame; zero remaining never proves completion.
const (
	snapshotBodyLimit  uint64 = math.MaxInt64 - sha256.Size
	snapshotFrameLimit uint64 = math.MaxInt64
)

type snapshotChunk uint8

const (
	snapshotData snapshotChunk = iota
	snapshotChecksum
)

// snapshotProof belongs to one receive executor. It retains bounded control
// evidence; the complete original protobuf frames must be captured separately
// before validation, including any frame rejected by these rules.
type snapshotProof struct {
	identity               Identity
	maxBody, maxFrames     uint64
	frames, expected, body uint64
	remaining              uint64
	version                string
	digest                 hash.Hash
	checksum               []byte
	started, awaitChecksum bool
	matched, eof, ended    bool
	failure                error
}

func newSnapshotProof(identity Identity, maxBody, maxFrames uint64) (*snapshotProof, error) {
	if identity.ClusterID == 0 || identity.MemberID == 0 || maxBody == 0 || maxBody > snapshotBodyLimit || maxFrames < 2 || maxFrames > snapshotFrameLimit {
		return nil, ErrInvalid
	}
	return &snapshotProof{identity: identity, maxBody: maxBody, maxFrames: maxFrames, digest: sha256.New()}, nil
}

func (p *snapshotProof) reject(err error) (snapshotChunk, error) {
	p.failure, p.ended = err, true
	return snapshotData, err
}

func (p *snapshotProof) accept(frame *pb.SnapshotResponse) (snapshotChunk, error) {
	if p.ended {
		return snapshotData, ErrUsed
	}
	p.frames++
	if frame == nil {
		return p.reject(ErrProtocol)
	}
	if p.frames > p.maxFrames || proto.Size(frame) > responseLimit {
		return p.reject(ErrBounds)
	}
	if p.matched {
		return p.reject(ErrProtocol) // a checksum must be followed by actual EOF
	}
	if h := frame.Header; h != nil {
		// Nil or unavailable header fields stay unavailable. Explicit original
		// identity fields may not identify another captured physical member.
		if h.ClusterId != 0 && h.ClusterId != p.identity.ClusterID || h.MemberId != 0 && h.MemberId != p.identity.MemberID || h.Revision < 0 {
			return p.reject(ErrProtocol)
		}
	}
	if frame.Version != "" {
		if p.version != "" && p.version != frame.Version {
			return p.reject(ErrProtocol)
		}
		p.version = frame.Version
	}
	if p.awaitChecksum {
		if frame.RemainingBytes != 0 || len(frame.Blob) != sha256.Size {
			return p.reject(ErrProtocol)
		}
		p.checksum = bytes.Clone(frame.Blob)
		if !bytes.Equal(p.digest.Sum(nil), p.checksum) {
			return p.reject(ErrProtocol)
		}
		p.matched = true
		return snapshotChecksum, nil
	}
	size := uint64(len(frame.Blob))
	if size == 0 {
		return p.reject(ErrProtocol)
	}
	// Subtraction bounds the addition even for an untrusted MaxUint64 count.
	if size > p.maxBody || frame.RemainingBytes > p.maxBody-size {
		return p.reject(ErrBounds)
	}
	if !p.started {
		p.expected = size + frame.RemainingBytes
		p.started = true
	} else if size+frame.RemainingBytes != p.remaining {
		return p.reject(ErrProtocol)
	}
	if size > p.expected-p.body {
		return p.reject(ErrProtocol)
	}
	p.body += size
	p.remaining = frame.RemainingBytes
	_, _ = p.digest.Write(frame.Blob)
	if p.remaining == 0 {
		if p.body != p.expected {
			return p.reject(ErrProtocol)
		}
		p.awaitChecksum = true
	}
	return snapshotData, nil
}

func (p *snapshotProof) finish(nativeError error, ctx context.Context) error {
	if p.ended {
		return ErrUsed
	}
	p.ended = true
	p.eof = nativeError == io.EOF
	if err := ctx.Err(); err != nil {
		p.failure = err
		return err
	}
	if nativeError != io.EOF {
		if nativeError == nil {
			nativeError = ErrProtocol
		}
		p.failure = nativeError
		return nativeError
	}
	if !p.started || !p.matched || p.body != p.expected || p.remaining != 0 {
		p.failure = ErrProtocol
		return ErrProtocol
	}
	return nil
}

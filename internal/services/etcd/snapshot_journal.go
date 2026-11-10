package etcd

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash"
	"io"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// The journal is independent of the concatenated native snapshot file. Each
// length-prefixed record contains bounded metadata or one complete protobuf
// frame, including its blob, nullable header and unknown extension fields.
const snapshotJournalMagic = "NyaDB-etcd-snapshot\x00\x01"

type SnapshotJournalKind byte

const (
	SnapshotJournalOriginKind SnapshotJournalKind = iota + 1
	SnapshotJournalFrameKind
	SnapshotJournalTerminalKind
	snapshotJournalEnd = SnapshotJournalTerminalKind
)

type SnapshotJournalOrigin struct {
	Identity                        Identity
	Limits                          SnapshotLimits
	Request, Status, StatusError    []byte
	StatusCode                      codes.Code
	StatusAttempted, StatusVerified bool
}

type SnapshotJournalTerminal struct {
	Attempted, NativeTerminalObserved, EOF, SourceComplete, ChecksumMatched bool
	OutputAcknowledged, Canceled                                            bool
	NativeStatus                                                            []byte
	NativeCode                                                              codes.Code
	Frames, ExpectedBodyBytes, BodyBytes, SnapshotBytesWritten              uint64
	BodySHA256, Checksum                                                    []byte
	StorageVersion, LocalStop, OutputError                                  string
}

type snapshotJournalTerminal = SnapshotJournalTerminal

func snapshotOrigin(id Identity, limits SnapshotLimits, before Response, nativeError error) SnapshotJournalOrigin {
	r := SnapshotJournalOrigin{Identity: id, Limits: limits, StatusCode: before.Code, StatusAttempted: before.Attempted, StatusVerified: before.Completed}
	r.Request, _ = proto.Marshal(&pb.SnapshotRequest{})
	if before.native != nil {
		r.Status, _ = proto.Marshal(before.native)
	}
	r.StatusError, _ = nativeSnapshotStatus(nativeError)
	return r
}

type snapshotJournalWriter struct {
	writer         io.Writer
	limit, written uint64
	digest         hash.Hash
	failed         error
}

func newSnapshotJournalWriter(w io.Writer, limit uint64) *snapshotJournalWriter {
	return &snapshotJournalWriter{writer: w, limit: limit, digest: sha256.New()}
}

func (j *snapshotJournalWriter) write(b []byte) error {
	if j.failed != nil {
		return j.failed // a partial record cannot be repaired by appending a footer
	}
	if uint64(len(b)) > j.limit-j.written {
		j.failed = ErrBounds
		return j.failed
	}
	n, err := j.writer.Write(b)
	if n < 0 || n > len(b) {
		n, err = 0, ErrProtocol
	}
	j.written += uint64(n)
	_, _ = j.digest.Write(b[:n])
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	j.failed = err
	return err
}

func (j *snapshotJournalWriter) start(origin SnapshotJournalOrigin) error {
	if err := j.write([]byte(snapshotJournalMagic)); err != nil {
		return err
	}
	return j.record(SnapshotJournalOriginKind, origin)
}

func (j *snapshotJournalWriter) record(kind SnapshotJournalKind, record any) error {
	raw, err := json.Marshal(record)
	if err != nil {
		j.failed = err
		return err
	}
	return j.raw(kind, raw)
}

func (j *snapshotJournalWriter) frame(raw []byte) error {
	return j.raw(SnapshotJournalFrameKind, raw)
}

func (j *snapshotJournalWriter) raw(kind SnapshotJournalKind, raw []byte) error {
	if j.failed != nil {
		return j.failed
	}
	// Account for the complete record before emitting its header. A budget
	// failure preserves the preceding complete records without a partial header.
	if len(raw) > responseLimit || uint64(len(raw))+9 > j.limit-j.written {
		j.failed = ErrBounds
		return j.failed
	}
	var header [9]byte
	header[0] = byte(kind)
	binary.BigEndian.PutUint64(header[1:], uint64(len(raw)))
	if err := j.write(header[:]); err != nil {
		return err
	}
	return j.write(raw)
}

// SnapshotJournalRecord contains fresh record bytes. Consumers may retain them;
// the reader never accumulates the complete stream or rebuilds write authority.
type SnapshotJournalRecord struct {
	Kind SnapshotJournalKind
	Raw  []byte
}

// ReadSnapshotJournal visits original records with bounded memory. A terminal
// record proves only that capture ended: inspect its native EOF/hash/local
// outcomes to decide source completeness. It never proves file publication.
// Truncation, duplicate origins, missing terminal and trailing bytes are errors.
func ReadSnapshotJournal(reader io.Reader, limit uint64, visit func(SnapshotJournalRecord) error) error {
	if reader == nil || visit == nil || limit < uint64(len(snapshotJournalMagic)) {
		return ErrInvalid
	}
	remaining := limit
	read := func(b []byte) error {
		if uint64(len(b)) > remaining {
			return ErrBounds
		}
		n, err := io.ReadFull(reader, b)
		remaining -= uint64(n)
		return err
	}
	magic := make([]byte, len(snapshotJournalMagic))
	if err := read(magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, []byte(snapshotJournalMagic)) {
		return ErrProtocol
	}
	first := true
	for {
		var header [9]byte
		if err := read(header[:]); err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		kind, size := SnapshotJournalKind(header[0]), binary.BigEndian.Uint64(header[1:])
		if size > responseLimit || size > remaining {
			return ErrBounds
		}
		if first && kind != SnapshotJournalOriginKind || !first && kind != SnapshotJournalFrameKind && kind != SnapshotJournalTerminalKind {
			return ErrProtocol
		}
		first = false
		raw := make([]byte, int(size))
		if err := read(raw); err != nil {
			return err
		}
		switch kind {
		case SnapshotJournalOriginKind:
			var origin SnapshotJournalOrigin
			if json.Unmarshal(raw, &origin) != nil || !origin.Limits.valid() || origin.Identity.ClusterID == 0 || origin.Identity.MemberID == 0 {
				return ErrProtocol
			}
		case SnapshotJournalFrameKind:
			if proto.Unmarshal(raw, &pb.SnapshotResponse{}) != nil {
				return ErrProtocol
			}
		case SnapshotJournalTerminalKind:
			if json.Unmarshal(raw, &SnapshotJournalTerminal{}) != nil {
				return ErrProtocol
			}
		}
		if err := visit(SnapshotJournalRecord{Kind: kind, Raw: raw}); err != nil {
			return err
		}
		if kind == SnapshotJournalTerminalKind {
			var extra [1]byte
			n, err := io.ReadFull(reader, extra[:])
			if n == 0 && err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			return ErrProtocol
		}
	}
}

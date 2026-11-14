package consul

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

// Private save-only archive foundation. None of these values publishes a
// provider, enters the generic HTTP receipt, or asserts native restore safety.
// The full archive (including ACL credentials) stays with its original owner.
type snapshotArchiveLimits struct{ compressed, expanded int64 }

func (b snapshotArchiveLimits) valid() bool {
	return b.compressed > 0 && b.compressed <= 64<<20 && b.expanded > 0 && b.expanded <= 256<<20
}

type snapshotArchiveProof struct {
	gzipComplete, tarComplete, checksumsMatch, metadataValid bool
	expandedBytes, stateBytes                                int64
	archiveHash, stateHash                                   [32]byte
	metadata, checksums                                      []byte // Exact private native bytes, never a normalized replacement.
	version, index, term                                     uint64
	id                                                       string
}

// Validate gzip before interpreting tar so transport EOF, gzip integrity, tar
// termination and native member checksums can never substitute for one another.
// Two bounded streaming passes avoid retaining the decompressed state in RAM.
func inspectSnapshotArchive(ctx context.Context, raw []byte, limits snapshotArchiveLimits) (proof snapshotArchiveProof, err error) {
	if !limits.valid() {
		return proof, ErrInvalid
	}
	if int64(len(raw)) > limits.compressed {
		return proof, ErrBounds
	}
	if err := ctx.Err(); err != nil {
		return proof, err
	}
	proof.archiveHash = sha256.Sum256(raw)
	compressed := bytes.NewReader(raw)
	z, err := gzip.NewReader(compressed)
	if err != nil {
		return proof, ErrProtocol
	}
	z.Multistream(false)
	n, readErr := io.Copy(io.Discard, io.LimitReader(&snapshotContextReader{ctx: ctx, reader: z}, limits.expanded+1))
	closeErr := z.Close()
	proof.expandedBytes = n
	if ctx.Err() != nil {
		return proof, ctx.Err()
	}
	if n > limits.expanded {
		return proof, ErrBounds
	}
	if readErr != nil || closeErr != nil || compressed.Len() != 0 {
		return proof, ErrProtocol
	}
	proof.gzipComplete = true
	z, err = gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return proof, ErrProtocol
	}
	defer z.Close()
	z.Multistream(false)
	counted := &snapshotContextReader{ctx: ctx, reader: z}
	archive := tar.NewReader(counted)
	seen := map[string]bool{}
	var metadataHash [32]byte
	var padding int64
	for {
		before := counted.bytes
		header, nextErr := archive.Next()
		if ctx.Err() != nil {
			return proof, ctx.Err()
		}
		if nextErr == io.EOF {
			// archive/tar also returns EOF for a missing terminator. Require both
			// original zero records, separately from gzip's own complete footer.
			if counted.bytes-before != padding+1024 || len(seen) != 3 {
				return proof, ErrProtocol
			}
			break
		}
		if nextErr != nil || counted.bytes-before != padding+512 || header.Typeflag != tar.TypeReg || header.Linkname != "" || len(header.PAXRecords) != 0 || seen[header.Name] {
			return proof, ErrProtocol
		}
		if header.Name != "meta.json" && header.Name != "state.bin" && header.Name != "SHA256SUMS" {
			return proof, ErrProtocol
		}
		if header.Size < 0 || header.Size > limits.expanded || header.Name == "meta.json" && header.Size > 64<<10 || header.Name == "SHA256SUMS" && header.Size > 1024 {
			return proof, ErrBounds
		}
		seen[header.Name] = true
		padding = (512 - header.Size%512) % 512
		switch header.Name {
		case "state.bin":
			h := sha256.New()
			n, err = io.Copy(h, archive)
			proof.stateBytes = n
			copy(proof.stateHash[:], h.Sum(nil))
		case "meta.json":
			proof.metadata, err = io.ReadAll(archive)
			n = int64(len(proof.metadata))
			metadataHash = sha256.Sum256(proof.metadata)
		case "SHA256SUMS":
			proof.checksums, err = io.ReadAll(archive)
			n = int64(len(proof.checksums))
		}
		if ctx.Err() != nil {
			return proof, ctx.Err()
		}
		if err != nil || n != header.Size {
			return proof, ErrProtocol
		}
	}
	// Native writers may add complete zero padding records. Reject a hidden
	// second tar, nonzero trailer or partial record instead of ignoring it.
	tail := &snapshotZeroWriter{}
	if _, err = io.Copy(tail, counted); err != nil || tail.nonzero || tail.bytes%512 != 0 {
		if ctx.Err() != nil {
			return proof, ctx.Err()
		}
		return proof, ErrProtocol
	}
	proof.tarComplete = true
	expected := map[string][32]byte{"meta.json": metadataHash, "state.bin": proof.stateHash}
	lines := strings.Split(string(proof.checksums), "\n")
	if len(lines) != 3 || lines[2] != "" {
		return proof, ErrProtocol
	}
	for _, line := range lines[:2] {
		// Preserve line order and exact raw spelling; accept the two standard
		// sha256sum separators, but no duplicate/extra names or path aliases.
		if len(line) < 67 || line[64] != ' ' || line[65] != ' ' && line[65] != '*' {
			return proof, ErrProtocol
		}
		name := line[66:]
		want, exists := expected[name]
		got, err := hex.DecodeString(line[:64])
		if !exists || err != nil || !bytes.Equal(got, want[:]) {
			return proof, ErrProtocol
		}
		delete(expected, name)
	}
	proof.checksumsMatch = true
	var fields map[string]json.RawMessage
	if jsondocument.Decode(proof.metadata, &fields, 64<<10) != nil {
		return proof, ErrProtocol
	}
	var size uint64
	for key, value := range map[string]*uint64{"Version": &proof.version, "Index": &proof.index, "Term": &proof.term, "Size": &size} {
		if !exactUint(string(fields[key])) || json.Unmarshal(fields[key], value) != nil {
			return proof, ErrProtocol
		}
	}
	if proof.version != 1 || json.Unmarshal(fields["ID"], &proof.id) != nil || proof.id == "" || !validText(proof.id, 1024) || size != uint64(proof.stateBytes) {
		return proof, ErrProtocol
	}
	proof.metadataValid = true
	return proof, nil
}

type snapshotContextReader struct {
	ctx    context.Context
	reader io.Reader
	bytes  int64
}

func (r *snapshotContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	r.bytes += int64(n)
	return n, err
}

type snapshotZeroWriter struct {
	bytes   int64
	nonzero bool
}

func (w *snapshotZeroWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		w.nonzero = w.nonzero || b != 0
	}
	w.bytes += int64(len(p))
	return len(p), nil
}

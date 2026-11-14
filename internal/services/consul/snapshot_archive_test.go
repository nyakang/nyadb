package consul

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"
)

var snapshotTestLimits = snapshotArchiveLimits{compressed: 1 << 20, expanded: 2 << 20}

type snapshotTestMember struct {
	name string
	body []byte
	kind byte
}

func snapshotTestMembers(state, metadata []byte, reverse bool) []snapshotTestMember {
	sums := fmt.Sprintf("%x  meta.json\n%x  state.bin\n", sha256.Sum256(metadata), sha256.Sum256(state))
	if reverse {
		sums = fmt.Sprintf("%x  state.bin\n%x  meta.json\n", sha256.Sum256(state), sha256.Sum256(metadata))
	}
	return []snapshotTestMember{{"meta.json", metadata, tar.TypeReg}, {"state.bin", state, tar.TypeReg}, {"SHA256SUMS", []byte(sums), tar.TypeReg}}
}

func snapshotTestTar(t *testing.T, members []snapshotTestMember) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0600, Size: int64(len(m.body)), Typeflag: m.kind}
		if m.kind == tar.TypeSymlink {
			h.Linkname, h.Size = "state.bin", 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size != 0 {
			if _, err := w.Write(m.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func snapshotTestGzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestSnapshotArchiveOriginalChecksumsOrdersAndExactMetadata(t *testing.T) {
	state := []byte("secret state\x00\xff")
	metadata := []byte(fmt.Sprintf("{\n\"Version\":1,\"ID\":\"original-id\",\"Index\":18446744073709551615,\"Term\":2,\"Size\":%d,\"Future\":{\"untouched\":true}}\n", len(state)))
	for _, reverse := range []bool{false, true} {
		members := snapshotTestMembers(state, metadata, reverse)
		archive := snapshotTestGzip(t, snapshotTestTar(t, members))
		proof, err := inspectSnapshotArchive(context.Background(), archive, snapshotTestLimits)
		if err != nil || !proof.gzipComplete || !proof.tarComplete || !proof.checksumsMatch || !proof.metadataValid || proof.index != ^uint64(0) || proof.stateBytes != int64(len(state)) || proof.stateHash != sha256.Sum256(state) || proof.archiveHash != sha256.Sum256(archive) {
			t.Fatal("original archive proof incomplete", err)
		}
		if !bytes.Equal(proof.metadata, metadata) || !bytes.Equal(proof.checksums, members[2].body) {
			t.Fatal("native evidence normalized")
		}
		clear(archive)
		if !bytes.Equal(proof.metadata, metadata) {
			t.Fatal("proof aliases caller archive")
		}
	}
}

func TestSnapshotArchiveRejectsIntegrityAndStructureCorruption(t *testing.T) {
	state := []byte("private\x00\xff")
	metadata := []byte(fmt.Sprintf(`{"Version":1,"ID":"original","Index":1,"Term":2,"Size":%d}`, len(state)))
	members := snapshotTestMembers(state, metadata, false)
	originalTar := snapshotTestTar(t, members)
	original := snapshotTestGzip(t, originalTar)
	cases := map[string][]byte{
		"empty":               nil,
		"gzip-truncated":      original[:len(original)-1],
		"gzip-extra":          append(bytes.Clone(original), 0),
		"gzip-second-stream":  append(bytes.Clone(original), snapshotTestGzip(t, nil)...),
		"tar-no-terminator":   snapshotTestGzip(t, originalTar[:len(originalTar)-1024]),
		"tar-one-terminator":  snapshotTestGzip(t, originalTar[:len(originalTar)-512]),
		"tar-partial-padding": snapshotTestGzip(t, append(bytes.Clone(originalTar), 0)),
		"tar-hidden-content":  snapshotTestGzip(t, append(bytes.Clone(originalTar), originalTar...)),
	}
	crc := bytes.Clone(original)
	crc[len(crc)-8] ^= 1
	cases["gzip-crc"] = crc
	for _, name := range []string{"duplicate", "missing", "path", "unknown", "link", "checksum", "checksum-duplicate", "checksum-extra"} {
		changed := append([]snapshotTestMember(nil), members...)
		switch name {
		case "duplicate":
			changed = append(changed, members[0])
		case "missing":
			changed = changed[:2]
		case "path":
			changed[0].name = "../meta.json"
		case "unknown":
			changed[0].name = "extra.json"
		case "link":
			changed[0].kind = tar.TypeSymlink
		case "checksum":
			changed[1].body = []byte("different")
		case "checksum-duplicate":
			changed[2].body = []byte(fmt.Sprintf("%x  meta.json\n%x  meta.json\n", sha256.Sum256(metadata), sha256.Sum256(metadata)))
		case "checksum-extra":
			changed[2].body = append(bytes.Clone(changed[2].body), []byte("extra\n")...)
		}
		cases[name] = snapshotTestGzip(t, snapshotTestTar(t, changed))
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			proof, err := inspectSnapshotArchive(context.Background(), raw, snapshotTestLimits)
			if !errors.Is(err, ErrProtocol) || proof.metadataValid {
				t.Fatal("corrupt archive accepted", err)
			}
		})
	}
	for _, padding := range []int{0, 512, 1024} {
		raw := snapshotTestGzip(t, append(bytes.Clone(originalTar), make([]byte, padding)...))
		if _, err := inspectSnapshotArchive(context.Background(), raw, snapshotTestLimits); err != nil {
			t.Fatal("complete zero records rejected", err)
		}
	}
}

func TestSnapshotArchiveChecksumsDoNotProveMetadataSemantics(t *testing.T) {
	for _, metadata := range []string{
		`{"Version":2,"ID":"id","Index":1,"Term":2,"Size":3}`,
		`{"Version":1,"ID":"id","Index":1,"Term":2,"Size":4}`,
		`{"Version":1,"ID":"id","Index":1,"Term":2,"Size":3,"Size":3}`,
		`{"Version":1,"ID":"id","Index":1e0,"Term":2,"Size":3}`,
		`{"Version":1,"ID":"id","Index":18446744073709551616,"Term":2,"Size":3}`,
		`{"Version":1,"ID":"id","Index":null,"Term":2,"Size":3}`,
		`{"Version":1,"ID":"","Index":1,"Term":2,"Size":3}`,
		`{"Version":1,"ID":"id","Index":1,"Size":3}`,
	} {
		raw := snapshotTestGzip(t, snapshotTestTar(t, snapshotTestMembers([]byte("abc"), []byte(metadata), false)))
		proof, err := inspectSnapshotArchive(context.Background(), raw, snapshotTestLimits)
		if !errors.Is(err, ErrProtocol) || !proof.gzipComplete || !proof.tarComplete || !proof.checksumsMatch || proof.metadataValid {
			t.Fatal("member integrity confused with metadata semantics", err)
		}
	}
}

func TestSnapshotArchiveBoundsAndCancellation(t *testing.T) {
	state := bytes.Repeat([]byte{0}, 64<<10)
	metadata := []byte(fmt.Sprintf(`{"Version":1,"ID":"id","Index":1,"Term":2,"Size":%d}`, len(state)))
	tarBytes := snapshotTestTar(t, snapshotTestMembers(state, metadata, false))
	raw := snapshotTestGzip(t, tarBytes)
	for _, limits := range []snapshotArchiveLimits{{int64(len(raw)) - 1, 2 << 20}, {1 << 20, int64(len(tarBytes)) - 1}} {
		proof, err := inspectSnapshotArchive(context.Background(), raw, limits)
		if !errors.Is(err, ErrBounds) || proof.gzipComplete {
			t.Fatal("capture or expansion bound ignored", err)
		}
	}
	if _, err := inspectSnapshotArchive(context.Background(), raw, snapshotArchiveLimits{int64(len(raw)), int64(len(tarBytes))}); err != nil {
		t.Fatal("exact limits rejected", err)
	}
	if _, err := inspectSnapshotArchive(context.Background(), raw, snapshotArchiveLimits{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid bounds accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspectSnapshotArchive(ctx, raw, snapshotTestLimits); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancelled validation continued")
	}
	reader := &snapshotContextReader{ctx: ctx, reader: bytes.NewReader(raw)}
	if n, err := io.Copy(io.Discard, reader); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reader consumed private bytes")
	}
}

package driverstore

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

type jdbcSnapshot struct {
	source, temporary, relative, digest string
	size                                int64
}

// ImportJDBC snapshots the explicitly selected local JDBC graph and derives a
// new bundle from the captured original agent/JRE package. Relative manifest
// dependencies retain their layout and exact bytes. Existing bundles and
// active leases are never changed; no external path reaches a later runtime.
func (s *Store) ImportJDBC(ctx context.Context, product Product, request connections.JDBCImport) (connections.ManagedDriver, error) {
	var zero connections.ManagedDriver
	request.Paths = append([]string(nil), request.Paths...)
	if ctx == nil || !component.MatchString(request.Profile) || !javaClass.MatchString(request.Class) || len(request.Paths) == 0 || len(request.Paths) > 128 {
		return zero, ErrPackage
	}
	lease, err := s.Acquire(ctx, product.Key, request.BaseVersion, request.BaseSHA256)
	if err != nil {
		return zero, err
	}
	defer lease.Close()
	m := lease.Manifest()
	if !product.accepts(m) || m.Runtime != "java" {
		return zero, ErrPackage
	}
	root, err := s.root()
	if err != nil {
		return zero, err
	}
	defer root.Close()
	stage := ".jdbc-" + rand.Text()
	if err := root.Mkdir(stage, 0700); err != nil {
		return zero, err
	}
	defer root.RemoveAll(stage)
	nodes, entries, err := snapshotJDBC(ctx, root, stage, request.Paths)
	if err != nil {
		return zero, err
	}
	type capturedFile struct{ Path, SHA256 string }
	files := make([]capturedFile, len(nodes))
	for i, node := range nodes {
		files[i] = capturedFile{node.relative, node.digest}
	}
	identity := struct {
		BaseSHA256, Profile, Class string
		Entries                    []string
		Files                      []capturedFile
	}{request.BaseSHA256, request.Profile, request.Class, entries, files}
	rawIdentity, _ := json.Marshal(identity)
	sum := sha256.Sum256(rawIdentity)
	digest := hex.EncodeToString(sum[:])
	prefix := "jdbc/" + digest[:24] + "/"
	m.Version = "jdbc-" + digest
	m.JDBC = &JDBCDriver{Class: request.Class}
	for _, entry := range entries {
		m.JDBC.Classpath = append(m.JDBC.Classpath, prefix+entry)
	}
	if !s.snapshot(m, request.BaseSHA256).Supports(request.Profile) {
		m.Profiles = append(m.Profiles, request.Profile)
	}
	provenance, _ := json.MarshalIndent(struct {
		Kind, ParentVersion, ParentSHA256, ParentProvenance, DriverClass string
		Entries                                                          []string
		Files                                                            []capturedFile
	}{"explicit-local-JDBC-snapshot", request.BaseVersion, request.BaseSHA256, m.Provenance, request.Class, entries, files}, "", "  ")
	provenance = append(provenance, '\n')
	m.Provenance = prefix + "sources.json"
	for _, node := range nodes {
		m.Files = append(m.Files, File{Path: prefix + node.relative, SHA256: node.digest})
	}
	provenanceHash := sha256.Sum256(provenance)
	m.Files = append(m.Files, File{Path: m.Provenance, SHA256: hex.EncodeToString(provenanceHash[:])})
	rawManifest, _ := json.MarshalIndent(m, "", "  ")
	if _, err := decodeManifest(rawManifest); err != nil {
		return zero, err
	}
	out, err := root.OpenFile(stage+"/package.zip", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return zero, err
	}
	defer out.Close()
	archive := zip.NewWriter(out)
	defer archive.Close()
	base, err := root.OpenRoot(lease.name)
	if err != nil {
		return zero, err
	}
	defer base.Close()
	var total int64
	for _, file := range lease.Manifest().Files {
		in, err := base.Open(file.Path)
		if err != nil {
			return zero, ErrPackage
		}
		n, err := writeJDBCEntry(ctx, archive, file, in, maxPackageBytes-total)
		in.Close()
		if err != nil {
			return zero, err
		}
		total += n
	}
	for _, node := range nodes {
		in, err := root.Open(node.temporary)
		if err != nil {
			return zero, ErrPackage
		}
		n, err := writeJDBCEntry(ctx, archive, File{Path: prefix + node.relative, SHA256: node.digest}, in, maxPackageBytes-total)
		in.Close()
		if err != nil {
			return zero, err
		}
		total += n
	}
	if _, err := writeJDBCEntry(ctx, archive, m.Files[len(m.Files)-1], strings.NewReader(string(provenance)), maxPackageBytes-total); err != nil {
		return zero, err
	}
	entry, err := archive.Create("manifest.json")
	if err != nil {
		return zero, err
	}
	if _, err = entry.Write(rawManifest); err != nil {
		return zero, err
	}
	if err = archive.Close(); err != nil {
		return zero, err
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	h := sha256.New()
	if _, err = copyBounded(ctx, h, out, maxPackageBytes); err != nil {
		return zero, err
	}
	archiveDigest := hex.EncodeToString(h.Sum(nil))
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	installed, err := s.install(ctx, out, archiveDigest, &product)
	if errors.Is(err, ErrExists) {
		original, verifyErr := s.Acquire(ctx, m.Key, m.Version, archiveDigest)
		if verifyErr != nil {
			return zero, verifyErr
		}
		installed = original.Manifest()
		err = original.Close()
	}
	if err != nil {
		return zero, err
	}
	return s.snapshot(installed, archiveDigest), nil
}

func writeJDBCEntry(ctx context.Context, archive *zip.Writer, file File, source io.Reader, limit int64) (int64, error) {
	mode := fs.FileMode(0600)
	if file.Executable {
		mode = 0700
	}
	header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
	header.SetMode(mode)
	out, err := archive.CreateHeader(header)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	n, err := copyBounded(ctx, io.MultiWriter(out, h), source, limit)
	if err == nil && hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
		err = ErrPackage
	}
	return n, err
}

func snapshotJDBC(ctx context.Context, root *os.Root, stage string, paths []string) ([]jdbcSnapshot, []string, error) {
	var entries, pending []string
	for _, selected := range paths {
		if !filepath.IsAbs(selected) || !strings.EqualFold(filepath.Ext(selected), ".jar") {
			return nil, nil, ErrPackage
		}
		original, err := filepath.EvalSymlinks(selected)
		if err != nil {
			return nil, nil, ErrPackage
		}
		entries = append(entries, original)
		pending = append(pending, original)
	}
	seen := map[string]bool{}
	var nodes []jdbcSnapshot
	var total int64
	for len(pending) != 0 {
		original := filepath.Clean(pending[0])
		pending = pending[1:]
		if seen[original] {
			continue
		}
		if len(nodes) >= 128 || !strings.EqualFold(filepath.Ext(original), ".jar") {
			return nil, nil, ErrPackage
		}
		seen[original] = true
		input, err := os.Open(original)
		if err != nil {
			return nil, nil, ErrPackage
		}
		stat, err := input.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			input.Close()
			return nil, nil, ErrPackage
		}
		temporary := stage + "/" + strconv.Itoa(len(nodes)) + ".jar"
		output, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			input.Close()
			return nil, nil, err
		}
		h := sha256.New()
		n, copyErr := copyBounded(ctx, io.MultiWriter(output, h), input, min(256<<20, maxPackageBytes-total))
		input.Close()
		dependencies, manifestErr := jdbcManifestDependencies(output, n)
		output.Close()
		if copyErr != nil {
			return nil, nil, copyErr
		}
		if manifestErr != nil {
			return nil, nil, manifestErr
		}
		total += n
		nodes = append(nodes, jdbcSnapshot{source: original, temporary: temporary, digest: hex.EncodeToString(h.Sum(nil)), size: n})
		for _, dependency := range dependencies {
			pending = append(pending, filepath.Join(filepath.Dir(original), filepath.FromSlash(dependency)))
		}
	}
	common := filepath.Dir(nodes[0].source)
	for _, node := range nodes {
		for {
			relative, err := filepath.Rel(common, node.source)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				break
			}
			parent := filepath.Dir(common)
			if parent == common {
				return nil, nil, ErrPackage
			}
			common = parent
		}
	}
	for i := range nodes {
		relative, _ := filepath.Rel(common, nodes[i].source)
		nodes[i].relative = filepath.ToSlash(relative)
		if !validPath("jdbc/snapshot/" + nodes[i].relative) {
			return nil, nil, ErrPackage
		}
	}
	for i := range entries {
		relative, _ := filepath.Rel(common, entries[i])
		entries[i] = filepath.ToSlash(relative)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].relative < nodes[j].relative })
	return nodes, entries, nil
}

// Read the main manifest section from the private captured JAR. Only relative
// local JAR references can be preserved in an immutable package; remote or
// absolute references are rejected before publishing any imported version.
func jdbcManifestDependencies(source io.ReaderAt, size int64) ([]string, error) {
	archive, err := zip.NewReader(source, size)
	if err != nil || len(archive.File) > 200000 {
		return nil, ErrPackage
	}
	var raw, index []byte
	for _, file := range archive.File {
		manifest := strings.EqualFold(file.Name, "META-INF/MANIFEST.MF")
		jarIndex := strings.EqualFold(file.Name, "META-INF/INDEX.LIST")
		if !manifest && !jarIndex {
			continue
		}
		if (manifest && raw != nil) || (jarIndex && index != nil) || file.UncompressedSize64 > 1<<20 {
			return nil, ErrPackage
		}
		reader, err := file.Open()
		if err != nil {
			return nil, ErrPackage
		}
		captured, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		reader.Close()
		if err != nil || len(captured) > 1<<20 {
			return nil, ErrPackage
		}
		if manifest {
			raw = captured
		} else {
			index = captured
		}
	}
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "" {
			break
		}
		if strings.HasPrefix(line, " ") {
			if len(lines) == 0 {
				return nil, ErrPackage
			}
			lines[len(lines)-1] += line[1:]
		} else {
			lines = append(lines, line)
		}
	}
	var references []string
	found := false
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ": ")
		if !ok || !strings.EqualFold(key, "Class-Path") {
			continue
		}
		if found {
			return nil, ErrPackage
		}
		found = true
		references = append(references, strings.Fields(value)...)
	}
	if index != nil {
		lines := strings.Split(strings.ReplaceAll(string(index), "\r\n", "\n"), "\n")
		if len(lines) < 2 || lines[0] != "JarIndex-Version: 1.0" {
			return nil, ErrPackage
		}
		first := false
		for _, line := range lines[1:] {
			if line == "" {
				first = true
				continue
			}
			if first {
				references = append(references, line)
				first = false
			}
		}
	}
	var dependencies []string
	for _, entry := range references {
		location, err := url.Parse(entry)
		if err != nil || location.IsAbs() || location.Host != "" || location.RawQuery != "" || location.Fragment != "" || strings.HasPrefix(location.Path, "/") || strings.ContainsAny(location.Path, "\\\x00") || !strings.EqualFold(filepath.Ext(location.Path), ".jar") {
			return nil, ErrPackage
		}
		dependencies = append(dependencies, location.Path)
	}
	return dependencies, nil
}

// JDBC resolves custom classpath entries only from the original verified lease.
func (l *Lease) JDBC() (string, []string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.manifest.JDBC == nil {
		return "", nil, ErrPackage
	}
	paths := make([]string, len(l.manifest.JDBC.Classpath))
	for i, entry := range l.manifest.JDBC.Classpath {
		paths[i] = filepath.Join(l.store.directory, l.name, filepath.FromSlash(entry))
	}
	return l.manifest.JDBC.Class, paths, nil
}

// Validate graphs from imported archives too, rather than trusting which
// importer produced the manifest. Every JVM-resolved dependency must be a
// declared original payload under jdbc/, including after a store restart.
func validateJDBCBundle(ctx context.Context, bundle *os.Root, m Manifest) error {
	if m.JDBC == nil {
		return nil
	}
	declared := make(map[string]bool, len(m.Files))
	for _, file := range m.Files {
		declared[file.Path] = true
	}
	seen := map[string]bool{}
	pending := append([]string(nil), m.JDBC.Classpath...)
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := pending[0]
		pending = pending[1:]
		if seen[entry] {
			continue
		}
		if len(seen) >= 128 || !strings.HasPrefix(entry, "jdbc/") || !declared[entry] || !strings.EqualFold(path.Ext(entry), ".jar") {
			return ErrPackage
		}
		seen[entry] = true
		file, err := bundle.Open(entry)
		if err != nil {
			return ErrPackage
		}
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 256<<20 {
			file.Close()
			return ErrPackage
		}
		dependencies, err := jdbcManifestDependencies(file, stat.Size())
		file.Close()
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			pending = append(pending, path.Join(path.Dir(entry), dependency))
		}
	}
	return nil
}

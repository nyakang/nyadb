package driverstore

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nyakang/nyadb/internal/services/agentbridge"
)

const maxPackageBytes = int64(1 << 30)

type Store struct{ directory string }

func New(directory string) (*Store, error) {
	if !filepath.IsAbs(directory) {
		return nil, ErrPackage
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	return &Store{directory: directory}, nil
}

func (s *Store) root() (*os.Root, error) { return os.OpenRoot(s.directory) }

func bundleName(key, version string) (string, error) {
	if !component.MatchString(key) || !component.MatchString(version) {
		return "", ErrPackage
	}
	return key + "@" + version, nil
}

// Install verifies the caller's complete archive digest before extracting. A
// version is published once by rename; existing versions are never overwritten.
func (s *Store) Install(ctx context.Context, source io.Reader, digest string) (Manifest, error) {
	return s.install(ctx, source, digest, nil)
}

// install checks product identity on the same private archive snapshot whose
// digest and payload are verified, before any installed version is published.
func (s *Store) install(ctx context.Context, source io.Reader, digest string, product *Product) (Manifest, error) {
	var m Manifest
	if ctx == nil || source == nil || !validDigest(digest) {
		return m, ErrPackage
	}
	root, err := s.root()
	if err != nil {
		return m, err
	}
	defer root.Close()
	stage := ".install-" + rand.Text()
	if err = root.Mkdir(stage, 0700); err != nil {
		return m, err
	}
	defer root.RemoveAll(stage)
	f, err := root.OpenFile(stage+"/input.zip", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return m, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := copyBounded(ctx, io.MultiWriter(f, h), source, maxPackageBytes)
	if err != nil {
		return m, err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return m, ErrPackage
	}
	z, err := zip.NewReader(f, n)
	if err != nil || len(z.File) > 20001 {
		return m, ErrPackage
	}
	var manifestFile *zip.File
	for _, entry := range z.File {
		if entry.Name == "manifest.json" {
			if manifestFile != nil {
				return m, ErrPackage
			}
			manifestFile = entry
		}
	}
	if manifestFile == nil || manifestFile.UncompressedSize64 > 1<<20 {
		return m, ErrPackage
	}
	reader, err := manifestFile.Open()
	if err != nil {
		return m, ErrPackage
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return m, ErrPackage
	}
	m, err = decodeManifest(raw)
	if err != nil {
		return m, err
	}
	if product != nil && !product.accepts(m) {
		return m, ErrPackage
	}
	if len(z.File) != len(m.Files)+1 {
		return m, ErrPackage
	}
	files := map[string]File{}
	for _, file := range m.Files {
		files[file.Path] = file
	}
	if err = root.Mkdir(stage+"/bundle", 0700); err != nil {
		return m, err
	}
	var total int64
	for _, entry := range z.File {
		if entry.Name == "manifest.json" {
			continue
		}
		file, ok := files[entry.Name]
		if !ok || !entry.Mode().IsRegular() || entry.UncompressedSize64 > uint64(maxPackageBytes-total) {
			return m, ErrPackage
		}
		delete(files, entry.Name)
		if err = root.MkdirAll(stage+"/bundle/"+path.Dir(file.Path), 0700); err != nil {
			return m, err
		}
		out, err := root.OpenFile(stage+"/bundle/"+file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return m, err
		}
		in, err := entry.Open()
		if err != nil {
			out.Close()
			return m, ErrPackage
		}
		h := sha256.New()
		size, copyErr := copyBounded(ctx, io.MultiWriter(out, h), in, maxPackageBytes-total)
		err = errors.Join(copyErr, in.Close(), out.Close())
		if err != nil {
			return m, err
		}
		if uint64(size) != entry.UncompressedSize64 || hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
			return m, ErrPackage
		}
		total += size
		mode := fs.FileMode(0600)
		if file.Executable {
			mode = 0700
		}
		if err = root.Chmod(stage+"/bundle/"+file.Path, mode); err != nil {
			return m, err
		}
	}
	if len(files) != 0 {
		return m, ErrPackage
	}
	if err = root.WriteFile(stage+"/bundle/manifest.json", raw, 0600); err != nil {
		return m, err
	}
	if err = root.WriteFile(stage+"/bundle/archive.sha256", []byte(digest), 0600); err != nil {
		return m, err
	}
	// Retain the original archive so a later acquisition can bind the installed
	// manifest to the caller's archive digest after an application restart.
	if err = f.Close(); err != nil {
		return m, err
	}
	if err = root.Rename(stage+"/input.zip", stage+"/bundle/.package.zip"); err != nil {
		return m, err
	}
	name, _ := bundleName(m.Key, m.Version)
	lock, err := lockStore(root)
	if err != nil {
		return m, err
	}
	defer unlockStore(lock)
	if err = ctx.Err(); err != nil {
		return m, err
	}
	if _, err = root.Lstat(name); err == nil {
		return m, ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return m, err
	}
	if err = root.Rename(stage+"/bundle", name); err != nil {
		return m, err
	}
	return cloneManifest(m), nil
}

func copyBounded(ctx context.Context, target io.Writer, source io.Reader, limit int64) (int64, error) {
	var total int64
	emptyReads := 0
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := source.Read(buffer)
		if n == 0 && err == nil {
			emptyReads++
			if emptyReads >= 100 {
				return total, io.ErrNoProgress
			}
		} else {
			emptyReads = 0
		}
		total += int64(n)
		if total > limit {
			return total, ErrPackage
		}
		if n > 0 {
			written, writeErr := target.Write(buffer[:n])
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, ctx.Err()
		}
		if err != nil {
			return total, err
		}
	}
}

// Lease pins an exact installed package. Close must follow original process
// acknowledgement. A crashed owner's marker remains busy for explicit recovery.
type Lease struct {
	mu           sync.Mutex
	store        *Store
	manifest     Manifest
	name, marker string
	closed       bool
	active       bool
	consumed     bool
}

func (s *Store) Acquire(ctx context.Context, key, version, digest string) (*Lease, error) {
	if ctx == nil || !validDigest(digest) {
		return nil, ErrPackage
	}
	name, err := bundleName(key, version)
	if err != nil {
		return nil, err
	}
	root, err := s.root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := lockStore(root)
	if err != nil {
		return nil, err
	}
	defer unlockStore(lock)
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil || !info.IsDir() {
		return nil, ErrPackage
	}
	bundle, err := root.OpenRoot(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, ErrPackage
	}
	defer bundle.Close()
	raw, err := readRegular(bundle, "manifest.json", 1<<20)
	if err != nil {
		return nil, err
	}
	m, err := decodeManifest(raw)
	if err != nil || m.Key != key || m.Version != version {
		return nil, ErrPackage
	}
	installedDigest, err := readRegular(bundle, "archive.sha256", 64)
	if err != nil || string(installedDigest) != digest {
		return nil, ErrPackage
	}
	archiveInfo, err := bundle.Lstat(".package.zip")
	if err != nil || !archiveInfo.Mode().IsRegular() || archiveInfo.Size() > maxPackageBytes {
		return nil, ErrPackage
	}
	archive, err := bundle.Open(".package.zip")
	if err != nil {
		return nil, ErrPackage
	}
	h := sha256.New()
	_, err = copyBounded(ctx, h, archive, maxPackageBytes)
	if err != nil {
		archive.Close()
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		archive.Close()
		return nil, ErrPackage
	}
	z, err := zip.NewReader(archive, archiveInfo.Size())
	if err != nil {
		archive.Close()
		return nil, ErrPackage
	}
	matched := false
	for _, entry := range z.File {
		if entry.Name != "manifest.json" {
			continue
		}
		if matched || entry.UncompressedSize64 > 1<<20 {
			archive.Close()
			return nil, ErrPackage
		}
		in, err := entry.Open()
		if err != nil {
			archive.Close()
			return nil, ErrPackage
		}
		original, err := io.ReadAll(io.LimitReader(in, (1<<20)+1))
		err = errors.Join(err, in.Close())
		if err != nil || string(original) != string(raw) {
			archive.Close()
			return nil, ErrPackage
		}
		matched = true
	}
	if err = archive.Close(); err != nil {
		return nil, err
	}
	if !matched {
		return nil, ErrPackage
	}
	var total int64
	for _, file := range m.Files {
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			info, err := bundle.Lstat(parent)
			if err != nil || !info.IsDir() {
				return nil, ErrPackage
			}
		}
		info, err := bundle.Lstat(file.Path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, ErrPackage
		}
		f, err := bundle.Open(file.Path)
		if err != nil {
			return nil, ErrPackage
		}
		h := sha256.New()
		n, err := copyBounded(ctx, h, f, maxPackageBytes-total)
		total += n
		err = errors.Join(err, f.Close())
		if err != nil {
			return nil, err
		}
		if hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
			return nil, ErrPackage
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = bundle.MkdirAll(".leases", 0700); err != nil {
		return nil, err
	}
	marker := ".leases/" + rand.Text()
	f, err := bundle.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		bundle.Remove(marker)
		return nil, err
	}
	return &Lease{store: s, manifest: m, name: name, marker: marker}, nil
}

func readRegular(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrPackage
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ErrPackage
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrPackage
	}
	return raw, nil
}

func (l *Lease) Manifest() Manifest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneManifest(l.manifest)
}

func (l *Lease) LaunchSpec(profile string) (agentbridge.LaunchSpec, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.launchSpec(profile)
}

func (l *Lease) launchSpec(profile string) (agentbridge.LaunchSpec, error) {
	if l.closed {
		return agentbridge.LaunchSpec{}, ErrMissing
	}
	found := false
	for _, p := range l.manifest.Profiles {
		found = found || p == profile
	}
	if !found {
		return agentbridge.LaunchSpec{}, ErrPackage
	}
	base := filepath.Join(l.store.directory, l.name)
	spec := agentbridge.LaunchSpec{Executable: filepath.Join(base, filepath.FromSlash(l.manifest.Executable))}
	for _, f := range l.manifest.Files {
		spec.Artifacts = append(spec.Artifacts, agentbridge.Artifact{Path: filepath.Join(base, filepath.FromSlash(f.Path)), SHA256: f.SHA256})
	}
	if l.manifest.Runtime == "java" {
		paths := make([]string, len(l.manifest.Classpath))
		for i, p := range l.manifest.Classpath {
			paths[i] = filepath.Join(base, filepath.FromSlash(p))
		}
		spec.Args = []string{"-cp", strings.Join(paths, string(os.PathListSeparator)), l.manifest.MainClass}
	}
	return spec, nil
}

func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.active {
		return ErrBusy
	}
	root, err := l.store.root()
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Remove(l.name + "/" + l.marker); err != nil {
		return err
	}
	l.closed = true
	return nil
}

func (s *Store) Remove(ctx context.Context, key, version string) error {
	if ctx == nil {
		return ErrPackage
	}
	name, err := bundleName(key, version)
	if err != nil {
		return err
	}
	root, err := s.root()
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := lockStore(root)
	if err != nil {
		return err
	}
	defer unlockStore(lock)
	if err = ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrMissing
	}
	if err != nil || !info.IsDir() {
		return ErrPackage
	}
	f, err := root.Open(name + "/.leases")
	if err == nil {
		entries, readErr := f.Readdirnames(1)
		f.Close()
		if len(entries) != 0 {
			return ErrBusy
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return root.RemoveAll(name)
}

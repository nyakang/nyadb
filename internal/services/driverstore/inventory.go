package driverstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/nyakang/nyadb/internal/services/connections"
)

// Product fixes the requested package identity before importing. These values
// are supplied by the adapter/host, not by an archive or form launch arguments.
type Product struct{ Key, Runtime, MainClass string }

func (p Product) accepts(m Manifest) bool {
	return component.MatchString(p.Key) && m.Key == p.Key && m.Runtime == p.Runtime && m.MainClass == p.MainClass
}

func (s *Store) snapshot(m Manifest, digest string) connections.ManagedDriver {
	d := connections.ManagedDriver{Key: m.Key, Version: m.Version, SHA256: digest, Directory: s.directory, Platform: m.Platform, Profiles: append([]string(nil), m.Profiles...)}
	if m.JDBC != nil {
		d.JDBCClass = m.JDBC.Class
	}
	return d
}

// List returns installed manifest/digest snapshots without launching or leasing
// a package. Acquisition still verifies the complete retained archive/payload.
func (s *Store) List(ctx context.Context, product Product) ([]connections.ManagedDriver, error) {
	if ctx == nil || !component.MatchString(product.Key) {
		return nil, ErrPackage
	}
	root, err := s.root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 4096 {
		return nil, ErrPackage
	}
	var out []connections.ManagedDriver
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(entry.Name(), product.Key+"@") {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, ErrPackage
		}
		bundle, err := root.OpenRoot(entry.Name())
		if err != nil {
			return nil, ErrPackage
		}
		raw, readErr := readRegular(bundle, "manifest.json", 1<<20)
		digest, digestErr := readRegular(bundle, "archive.sha256", 64)
		bundle.Close()
		m, decodeErr := decodeManifest(raw)
		name, nameErr := bundleName(m.Key, m.Version)
		if readErr != nil || digestErr != nil || decodeErr != nil || nameErr != nil || name != entry.Name() || !product.accepts(m) || !validDigest(string(digest)) {
			return nil, ErrPackage
		}
		out = append(out, s.snapshot(m, string(digest)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, ctx.Err()
}

// Import pins the complete selected local archive, then validates the requested
// product against the installer's private digest-verified archive snapshot.
// A concurrently changed file cannot publish a different package. Reimporting
// an identical installed version is idempotent; replacements remain rejected.
func (s *Store) Import(ctx context.Context, product Product, file string) (connections.ManagedDriver, error) {
	var zero connections.ManagedDriver
	if ctx == nil || !component.MatchString(product.Key) {
		return zero, ErrPackage
	}
	f, err := os.Open(file)
	if err != nil {
		return zero, ErrPackage
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPackageBytes {
		return zero, ErrPackage
	}
	h := sha256.New()
	_, err = copyBounded(ctx, h, f, maxPackageBytes)
	if err != nil {
		return zero, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return zero, ErrPackage
	}
	installed, err := s.install(ctx, f, digest, &product)
	if errors.Is(err, ErrExists) {
		lease, verifyErr := s.Acquire(ctx, installed.Key, installed.Version, digest)
		if verifyErr != nil {
			return zero, verifyErr
		}
		installed = lease.Manifest()
		if err := lease.Close(); err != nil {
			return zero, err
		}
		err = nil
	}
	if err != nil {
		return zero, err
	}
	return s.snapshot(installed, digest), nil
}

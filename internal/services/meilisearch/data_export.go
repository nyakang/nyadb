package meilisearch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const exportHashMemory = 8 << 20

// Export identities spill into private partitions, so duplicate checks do not
// retain a growing in-memory set. Large partitions split until bounded.
func validateExportHashes(ctx context.Context, path string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if info.Size()%sha256.Size != 0 {
		return ErrProtocol
	}
	if info.Size() <= exportHashMemory {
		seen := map[[sha256.Size]byte]bool{}
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			var hash [sha256.Size]byte
			_, err = io.ReadFull(f, hash[:])
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if seen[hash] {
				return ErrProtocol
			}
			seen[hash] = true
		}
	}
	if depth >= sha256.Size {
		return ErrProtocol
	}
	directory := path + "-parts"
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	files := map[byte]*os.File{}
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	defer closeFiles()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var hash [sha256.Size]byte
		_, err = io.ReadFull(f, hash[:])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		file := files[hash[depth]]
		if file == nil {
			file, err = os.OpenFile(filepath.Join(directory, hex.EncodeToString(hash[depth:depth+1])), os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			files[hash[depth]] = file
		}
		if _, err = file.Write(hash[:]); err != nil {
			return err
		}
	}
	for key, file := range files {
		if err = file.Close(); err != nil {
			return err
		}
		delete(files, key)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = validateExportHashes(ctx, filepath.Join(directory, entry.Name()), depth+1); err != nil {
			return err
		}
	}
	return nil
}

// ownedExportSpool exposes no rows until the entire native traversal and all
// duplicate checks succeed. Final destination I/O is governed by its host sink.
type ownedExportSpool struct {
	directory string
	source    *os.File
	hashes    map[byte]*os.File
}

func newExportSpool() (*ownedExportSpool, error) {
	directory, err := os.MkdirTemp("", "nyadb-meilisearch-export-")
	if err != nil {
		return nil, err
	}
	source, err := os.OpenFile(filepath.Join(directory, "source.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &ownedExportSpool{directory: directory, source: source, hashes: map[byte]*os.File{}}, nil
}
func (s *ownedExportSpool) close() {
	for _, f := range s.hashes {
		_ = f.Close()
	}
	_ = s.source.Close()
	_ = os.RemoveAll(s.directory)
}
func (s *ownedExportSpool) add(id string, raw []byte) error {
	hash := sha256.Sum256([]byte(id))
	file := s.hashes[hash[0]]
	if file == nil {
		var err error
		file, err = os.OpenFile(filepath.Join(s.directory, hex.EncodeToString(hash[:1])), os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		s.hashes[hash[0]] = file
	}
	if _, err := file.Write(hash[:]); err != nil {
		return err
	}
	if _, err := s.source.Write(raw); err != nil {
		return err
	}
	_, err := s.source.Write([]byte{'\n'})
	return err
}
func (s *ownedExportSpool) validate(ctx context.Context) error {
	for key, file := range s.hashes {
		if err := file.Close(); err != nil {
			return err
		}
		delete(s.hashes, key)
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "source.jsonl" {
			continue
		}
		if err = validateExportHashes(ctx, filepath.Join(s.directory, entry.Name()), 1); err != nil {
			return err
		}
	}
	_, err = s.source.Seek(0, io.SeekStart)
	return err
}
func (s *ownedExportSpool) emit(ctx context.Context, sink func([]byte) error) error {
	reader := bufio.NewReaderSize(s.source, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if len(line) > responseLimit+1 {
			return ErrBounds
		}
		if err = sink(line[:len(line)-1]); err != nil {
			return err
		}
	}
}

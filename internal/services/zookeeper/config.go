// Package zookeeper adapts the original managed native agent without SQL or
// public chooser/provider registration. Each document owns its original process.
package zookeeper

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/nyakang/nyadb/internal/services/connections"
)

var (
	ErrInvalid    = errors.New("invalid native ZooKeeper configuration or request")
	ErrBounds     = errors.New("native ZooKeeper resource limit exceeded")
	ErrProjection = errors.New("native ZooKeeper receipt projection is incomplete")
)

const nativeProtocol = "zookeeper-jsonrpc-v1"
const frameLimit = 32 << 20
const requestLimit = 2 << 20
const valueLimit = 1 << 20

type Config = connections.ZooKeeperConfig

func normalizePath(p string) string { return connections.NormalizeZNodePath(p) }
func validPath(p string) bool       { return connections.ValidZNodePath(p) }
func normalizeConfig(c Config) (Config, string, error) {
	c, root, err := connections.NormalizeZooKeeperConfig(c)
	if errors.Is(err, connections.ErrZooKeeperBounds) {
		return c, root, ErrBounds
	}
	if err != nil {
		return c, root, ErrInvalid
	}
	return c, root, nil
}

type capturedConfig struct {
	config                       Config
	root, fingerprint, directory string
}

func captureConfig(c Config) (capturedConfig, error) {
	c, root, err := normalizeConfig(c)
	if err != nil {
		return capturedConfig{}, err
	}
	original := c.Clone()
	var materials [3][]byte
	for i, path := range []string{c.CACertPath, c.ClientCertPath, c.ClientKeyPath} {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return capturedConfig{}, ErrInvalid
		}
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1<<20 {
			f.Close()
			return capturedConfig{}, ErrBounds
		}
		materials[i] = make([]byte, stat.Size())
		_, err = io.ReadFull(f, materials[i])
		f.Close()
		if err != nil {
			return capturedConfig{}, ErrInvalid
		}
	}
	if materials[0] != nil && !x509.NewCertPool().AppendCertsFromPEM(materials[0]) {
		return capturedConfig{}, ErrInvalid
	}
	if materials[1] != nil {
		if _, err := tls.X509KeyPair(materials[1], materials[2]); err != nil {
			return capturedConfig{}, ErrInvalid
		}
	}
	var hashes [3]string
	for i, b := range materials {
		if b != nil {
			h := sha256.Sum256(b)
			hashes[i] = hex.EncodeToString(h[:])
		}
	}
	original.CACertPath = ""
	original.ClientCertPath = ""
	original.ClientKeyPath = ""
	raw, _ := json.Marshal(struct {
		Config Config
		Prefix string
		Trust  [3]string
	}{original, c.Prefix, hashes})
	h := sha256.Sum256(raw)
	result := capturedConfig{config: c, root: root, fingerprint: hex.EncodeToString(h[:])}
	if materials[0] != nil || materials[1] != nil {
		result.directory, err = os.MkdirTemp("", "nyadb-zookeeper-trust-")
		if err != nil {
			return capturedConfig{}, ErrInvalid
		}
		for i, b := range materials {
			if b == nil {
				continue
			}
			path := filepath.Join(result.directory, strconv.Itoa(i)+".pem")
			if err = os.WriteFile(path, b, 0600); err != nil {
				os.RemoveAll(result.directory)
				return capturedConfig{}, ErrInvalid
			}
			switch i {
			case 0:
				result.config.CACertPath = path
			case 1:
				result.config.ClientCertPath = path
			case 2:
				result.config.ClientKeyPath = path
			}
		}
	}
	return result, nil
}

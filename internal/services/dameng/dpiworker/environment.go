package dpiworker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const nativeConfig = "# NyaDB original physical session: private native defaults.\n"
const sslConfig = "# NyaDB private native OpenSSL defaults.\n"

func prepareDirectory() (string, error) {
	dir, err := os.MkdirTemp("", "nyadb-dpi-worker-")
	if err != nil {
		return "", err
	}
	if len(filepath.Join(dir, "dm_svc.conf")) > 256 {
		os.RemoveAll(dir)
		return "", errors.New("DM8 private native configuration path exceeds the client bound")
	}
	for name, content := range map[string]string{"dm_svc.conf": nativeConfig, "openssl.cnf": sslConfig} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// Construct a new environment without editing the workbench's environment.
// Native service paths and OpenSSL configuration must refer to existing,
// privately owned files: a missing file would fall back to /etc/dm_svc.conf.
func workerEnvironment(parent []string, dir string) []string {
	out := make([]string, 0, 8)
	for _, v := range parent {
		key, _, ok := strings.Cut(v, "=")
		if ok && (key == "PATH" || key == "LD_LIBRARY_PATH" || key == "SYSTEMROOT" || key == "WINDIR" || key == "TZ") {
			out = append(out, v)
		}
	}
	return append(out, "DM_SVC_PATH="+dir, "OPENSSL_CONF="+filepath.Join(dir, "openssl.cnf"), "TMPDIR="+dir, "LANG=C.UTF-8", "LC_ALL=C.UTF-8")
}

func validateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("DM8 isolated native directory is unavailable")
	}
	for name, expected := range map[string]string{"dm_svc.conf": nativeConfig, "openssl.cnf": sslConfig} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != int64(len(expected)) {
			return errors.New("DM8 isolated native configuration changed")
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, []byte(expected)) {
			return errors.New("DM8 isolated native configuration changed")
		}
	}
	return nil
}

func validateEnvironment(dir string) error {
	if err := validateDirectory(dir); err != nil {
		return err
	}
	if os.Getenv("DM_SVC_PATH") != dir || os.Getenv("OPENSSL_CONF") != filepath.Join(dir, "openssl.cnf") {
		return errors.New("DM8 isolated native environment differs from its owned configuration")
	}
	return nil
}

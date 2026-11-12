// Package traefik generates Traefik's configuration and controls the
// Traefik process that runs next to the controller.
//
// Traefik has two kinds of configuration:
//
//   - static (entry points, certificate resolver): read once at start
//     from a file. Changing it requires a restart of Traefik.
//   - dynamic (routers, services): polled from the controller over HTTP
//     every few seconds and applied without a restart.
package traefik

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Paths are the files and directories shared with the Traefik process.
// All of them live in the data volume.
type Paths struct {
	StaticFile string // Traefik's static configuration
	ACMEDir    string // certificates and ACME account, written by Traefik
	LogFile    string // Traefik's log, read by the controller
	DNSEnvDir  string // DNS provider credentials, one file per variable
}

// PathsIn returns the standard layout below dataDir.
func PathsIn(dataDir string) Paths {
	return Paths{
		StaticFile: filepath.Join(dataDir, "traefik", "traefik.yml"),
		ACMEDir:    filepath.Join(dataDir, "acme"),
		LogFile:    filepath.Join(dataDir, "logs", "traefik.log"),
		DNSEnvDir:  filepath.Join(dataDir, "secrets", "dns-env"),
	}
}

// ACMEFile returns the certificate store for the chosen Let's Encrypt
// environment. Staging and production use separate files, so switching
// back and forth never mixes test certificates with real ones.
func (p Paths) ACMEFile(staging bool) string {
	if staging {
		return filepath.Join(p.ACMEDir, "acme-staging.json")
	}
	return filepath.Join(p.ACMEDir, "acme-production.json")
}

// EnsureDirs creates all directories. Only the owner may enter them:
// they contain credentials and private keys.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{filepath.Dir(p.StaticFile), p.ACMEDir, filepath.Dir(p.LogFile), p.DNSEnvDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes data to a temporary file and then renames it
// over path. A reader therefore sees either the old or the new content,
// never a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	// CreateTemp creates the file with mode 0600, so the content is
	// never readable by others, not even for a moment.
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// CheckDNSEnv validates the credentials of a DNS provider: keys must
// look like environment variable names (they become file names), values
// must fit on one line.
func CheckDNSEnv(env map[string]string) error {
	for key, value := range env {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("%q ist kein gültiger Variablenname (erlaubt: A-Z, 0-9, _)", key)
		}
		if value == "" || strings.ContainsAny(value, "\n\r\x00") {
			// The message names the key only, never the value.
			return fmt.Errorf("der Wert von %s ist leer oder enthält einen Zeilenumbruch", key)
		}
	}
	return nil
}

// WriteDNSEnv replaces the stored DNS credentials with env. Each
// variable becomes a file named like the variable, containing the
// value, with mode 0600. Traefik is started through "s6-envdir", which
// turns exactly this layout into environment variables, so the secrets
// never pass through a shell or a config file.
func WriteDNSEnv(dir string, env map[string]string) error {
	if err := CheckDNSEnv(env); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	old, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range old {
		if _, keep := env[entry.Name()]; !keep {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	for key, value := range env {
		if err := writeFileAtomic(filepath.Join(dir, key), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ReadDNSEnv returns the stored DNS credentials. Callers must treat the
// values as secrets: no logging, no API responses.
func ReadDNSEnv(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !envKeyPattern.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		env[entry.Name()] = strings.TrimRight(string(data), "\r\n")
	}
	return env, nil
}

package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// env turns a map into a lookup function like os.Getenv.
func env(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		EnvControllerURL: "http://192.0.2.10:8080",
		EnvToken:         " secret\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ControllerURL.String(); got != "http://192.0.2.10:8080" {
		t.Errorf("ControllerURL = %q", got)
	}
	if cfg.Token != "secret" {
		t.Errorf("Token = %q, want surrounding whitespace removed", cfg.Token)
	}
	if cfg.HostIP != "" {
		t.Errorf("HostIP = %q, want empty (auto-detect)", cfg.HostIP)
	}
	if cfg.Interval != defaultInterval {
		t.Errorf("Interval = %s, want %s", cfg.Interval, defaultInterval)
	}
	if cfg.DockerSocket != defaultDockerSocket {
		t.Errorf("DockerSocket = %q", cfg.DockerSocket)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %s", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		EnvControllerURL: "https://proxy.example.com",
		EnvToken:         "secret",
		EnvHostIP:        "192.0.2.20",
		EnvInterval:      "2m",
		EnvDockerSocket:  "/run/user/1000/docker.sock",
		EnvLogLevel:      "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HostIP != "192.0.2.20" || cfg.Interval != 2*time.Minute ||
		cfg.DockerSocket != "/run/user/1000/docker.sock" || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(env(map[string]string{
		EnvControllerURL: "http://192.0.2.10:8080",
		EnvTokenFile:     path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "from-file" {
		t.Errorf("Token = %q, want %q", cfg.Token, "from-file")
	}
}

func TestLoadErrors(t *testing.T) {
	valid := map[string]string{
		EnvControllerURL: "http://192.0.2.10:8080",
		EnvToken:         "secret",
	}
	tests := []struct {
		name    string
		change  map[string]string // applied on top of valid
		wantErr string
	}{
		{"missing url", map[string]string{EnvControllerURL: ""}, EnvControllerURL},
		{"url without scheme", map[string]string{EnvControllerURL: "192.0.2.10:8080"}, EnvControllerURL},
		{"unsupported scheme", map[string]string{EnvControllerURL: "ftp://192.0.2.10"}, EnvControllerURL},
		{"missing token", map[string]string{EnvToken: ""}, EnvToken},
		{"token and token file", map[string]string{EnvTokenFile: "/nonexistent"}, "only one"},
		{"bad host ip", map[string]string{EnvHostIP: "not-an-ip"}, EnvHostIP},
		{"bad interval", map[string]string{EnvInterval: "30"}, EnvInterval},
		{"interval too short", map[string]string{EnvInterval: "10ms"}, "at least"},
		{"bad log level", map[string]string{EnvLogLevel: "loud"}, EnvLogLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := make(map[string]string)
			for k, v := range valid {
				vars[k] = v
			}
			for k, v := range tt.change {
				vars[k] = v
			}
			_, err := Load(env(vars))
			if err == nil {
				t.Fatal("Load() = nil error, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error %q leaks the token", err)
			}
		})
	}
}

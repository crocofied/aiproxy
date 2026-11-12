// Package config reads the agent's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// Names of the environment variables the agent understands.
const (
	EnvControllerURL = "LANPROXY_CONTROLLER_URL"
	EnvToken         = "LANPROXY_AGENT_TOKEN"
	EnvTokenFile     = "LANPROXY_AGENT_TOKEN_FILE"
	EnvHostIP        = "LANPROXY_HOST_IP"
	EnvInterval      = "LANPROXY_HEARTBEAT_INTERVAL"
	EnvDockerSocket  = "LANPROXY_DOCKER_SOCKET"
	EnvLogLevel      = "LANPROXY_LOG_LEVEL"
)

const (
	defaultInterval     = 30 * time.Second
	defaultDockerSocket = "/var/run/docker.sock"
	minInterval         = time.Second
)

// Config holds the validated settings of the agent.
type Config struct {
	ControllerURL *url.URL
	// Token authenticates the agent to the controller. It is a secret:
	// never log it and never put it into an error message.
	Token string
	// HostIP is empty if the address should be detected automatically.
	HostIP       string
	Interval     time.Duration
	DockerSocket string
	LogLevel     slog.Level
}

// Load builds a Config from environment variables. It takes the lookup
// function as a parameter (pass os.Getenv) so that tests need not modify
// the real environment.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Interval:     defaultInterval,
		DockerSocket: defaultDockerSocket,
		LogLevel:     slog.LevelInfo,
	}

	rawURL := getenv(EnvControllerURL)
	if rawURL == "" {
		return Config{}, fmt.Errorf("%s is required", EnvControllerURL)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvControllerURL, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Config{}, fmt.Errorf("%s must look like http://host:port or https://host", EnvControllerURL)
	}
	cfg.ControllerURL = u

	if cfg.Token, err = loadToken(getenv); err != nil {
		return Config{}, err
	}

	if ip := getenv(EnvHostIP); ip != "" {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvHostIP, err)
		}
		cfg.HostIP = addr.String()
	}

	if raw := getenv(EnvInterval); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w (examples: 30s, 2m)", EnvInterval, err)
		}
		if d < minInterval {
			return Config{}, fmt.Errorf("%s must be at least %s", EnvInterval, minInterval)
		}
		cfg.Interval = d
	}

	if socket := getenv(EnvDockerSocket); socket != "" {
		cfg.DockerSocket = socket
	}

	if raw := getenv(EnvLogLevel); raw != "" {
		// UnmarshalText understands "debug", "info", "warn" and "error".
		if err := cfg.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvLogLevel, err)
		}
	}
	return cfg, nil
}

// loadToken reads the token either directly from the environment or from
// a file, which is how Docker secrets are passed to a container.
func loadToken(getenv func(string) string) (string, error) {
	token := getenv(EnvToken)
	if path := getenv(EnvTokenFile); path != "" {
		if token != "" {
			return "", fmt.Errorf("set only one of %s and %s", EnvToken, EnvTokenFile)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", EnvTokenFile, err)
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New(EnvToken + " (or " + EnvTokenFile + ") is required")
	}
	return token, nil
}

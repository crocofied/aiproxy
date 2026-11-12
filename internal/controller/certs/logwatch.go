package certs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// LogWatcher follows Traefik's log file. It serves two purposes:
//
//   - Traefik has no API that says "the last renewal failed"; the only
//     place this shows up is its log. The watcher remembers the most
//     recent ACME error for the UI.
//   - It copies Traefik's messages into the controller's own log, so
//     that "docker logs" shows both processes.
type LogWatcher struct {
	path string
	log  *slog.Logger
	// secrets returns values that must never appear in output. ACME
	// libraries occasionally quote API requests in error messages.
	secrets func() []string

	mu          sync.Mutex
	lastError   string
	lastErrorAt time.Time
}

// NewLogWatcher returns a watcher for the log file at path.
func NewLogWatcher(path string, secrets func() []string, log *slog.Logger) *LogWatcher {
	return &LogWatcher{path: path, log: log, secrets: secrets}
}

// LastACMEError returns the most recent certificate error and when it
// happened; the message is empty if there was none.
func (w *LogWatcher) LastACMEError() (message string, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastError, w.lastErrorAt
}

// Run follows the log until ctx is cancelled.
func (w *LogWatcher) Run(ctx context.Context) {
	// Lines that already exist when we start are only searched for
	// errors, not printed again.
	offset := w.read(0, false)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			offset = w.read(offset, true)
		}
	}
}

// read processes all complete lines after offset and returns the new
// offset.
func (w *LogWatcher) read(offset int64, forward bool) int64 {
	f, err := os.Open(w.path)
	if err != nil {
		return 0 // not created yet, or rotated away: start over next time
	}
	defer f.Close()

	if info, err := f.Stat(); err != nil || info.Size() < offset {
		offset = 0 // the file was rotated and is a new, shorter one
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// io.EOF with a partial line: Traefik is still writing it.
			// Leave it for the next round.
			if !errors.Is(err, io.EOF) {
				w.log.Warn("read traefik log", "error", err)
			}
			return offset
		}
		offset += int64(len(line))
		w.handle(line, forward)
	}
}

// logLine is the subset of Traefik's JSON log format we look at.
type logLine struct {
	Level    string `json:"level"`
	Time     string `json:"time"`
	Message  string `json:"message"`
	Error    string `json:"error"`
	Provider string `json:"providerName"`
}

func (w *LogWatcher) handle(raw []byte, forward bool) {
	var line logLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return // not JSON, e.g. a panic trace
	}
	line.Message = w.redact(line.Message)
	line.Error = w.redact(line.Error)

	if line.Level == "error" && isACME(line) {
		at, err := time.Parse(time.RFC3339, line.Time)
		if err != nil {
			at = time.Now()
		}
		text := line.Message
		if line.Error != "" {
			text += ": " + line.Error
		}
		w.mu.Lock()
		w.lastError, w.lastErrorAt = text, at
		w.mu.Unlock()
	}

	if !forward {
		return
	}
	level := slog.LevelInfo
	switch line.Level {
	case "debug", "trace":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error", "fatal", "panic":
		level = slog.LevelError
	}
	attrs := []any{}
	if line.Error != "" {
		attrs = append(attrs, "error", line.Error)
	}
	w.log.Log(context.Background(), level, "traefik: "+line.Message, attrs...)
}

// isACME reports whether a log line comes from certificate handling.
func isACME(line logLine) bool {
	return strings.HasSuffix(line.Provider, ".acme") ||
		strings.Contains(line.Message, "ACME") ||
		strings.Contains(strings.ToLower(line.Message), "certificate")
}

// redact replaces every known secret in s.
func (w *LogWatcher) redact(s string) string {
	if s == "" || w.secrets == nil {
		return s
	}
	for _, secret := range w.secrets() {
		// Very short values would also match harmless text.
		if len(secret) >= 6 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

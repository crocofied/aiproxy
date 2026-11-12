package hostip

import (
	"context"
	"net/url"
	"testing"
)

func TestDetect(t *testing.T) {
	// The route to a loopback address always starts at loopback, so this
	// works on any machine, with or without a network.
	tests := []struct {
		controller string
		want       string
	}{
		{"http://127.0.0.1:8080", "127.0.0.1"},
		{"http://127.0.0.1", "127.0.0.1"},  // default port for http
		{"https://127.0.0.1", "127.0.0.1"}, // default port for https
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.controller)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Detect(context.Background(), u)
		if err != nil {
			t.Errorf("Detect(%s) error: %v", tt.controller, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Detect(%s) = %q, want %q", tt.controller, got, tt.want)
		}
	}
}

func TestDetectUnresolvableHost(t *testing.T) {
	// ".invalid" is reserved and never resolves (RFC 2606).
	u, _ := url.Parse("http://controller.invalid:8080")
	if _, err := Detect(context.Background(), u); err == nil {
		t.Error("Detect() = nil error, want a failure for an unresolvable host")
	}
}

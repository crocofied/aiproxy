// Package routing holds the rules that turn discovered containers and
// stored routes into proxy targets: hostnames, labels, conflicts, status.
//
// The functions here are pure (no database, no network), which makes
// them easy to test. Messages returned to the admin are in German, like
// the rest of the user interface.
package routing

import (
	"errors"
	"fmt"
	"strings"

	"lanproxy/internal/controller/store"
)

// NormalizeHostname lower-cases a hostname and removes surrounding
// space and a trailing dot.
func NormalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// ValidLabel reports whether s is a valid DNS label: 1 to 63 characters
// from a-z, 0-9 and "-", not starting or ending with "-".
func ValidLabel(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// ValidDNSName reports whether host is a syntactically valid, lower-case
// hostname with at least two labels. Because of this check a hostname
// can be placed in a Traefik rule without any escaping.
func ValidDNSName(host string) bool {
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !ValidLabel(label) {
			return false
		}
	}
	return true
}

// CheckHostname decides whether a route may use host. Besides the
// syntax, the name must be covered by the wildcard certificate, which
// only spans one level: "*.example.com" matches "a.example.com" but not
// "a.b.example.com".
func CheckHostname(host string, s store.Settings) error {
	if !ValidDNSName(host) {
		return fmt.Errorf("%q ist kein gültiger Hostname", host)
	}
	if s.BaseDomain == "" {
		return errors.New("die Basis-Domain ist noch nicht eingerichtet")
	}
	if host == s.UIHost() {
		return fmt.Errorf("%q ist für die Oberfläche reserviert", host)
	}
	if host == s.BaseDomain {
		return nil
	}
	sub, ok := strings.CutSuffix(host, "."+s.BaseDomain)
	if !ok {
		return fmt.Errorf("%q liegt nicht unter der Basis-Domain %s", host, s.BaseDomain)
	}
	if strings.Contains(sub, ".") {
		return fmt.Errorf("%q hat eine Ebene zu viel: das Wildcard-Zertifikat deckt nur *.%s ab", host, s.BaseDomain)
	}
	return nil
}

// PatternVars are the values available to a hostname pattern.
type PatternVars struct {
	Service   string // compose service, or the container name without compose
	Project   string // compose project
	Container string // container name
	Agent     string // name of the agent
	Domain    string // base domain
}

// ExpandPattern replaces the placeholders {service}, {project},
// {container}, {agent} and {domain} in pattern. All values except the
// domain are first converted into valid DNS labels.
func ExpandPattern(pattern string, v PatternVars) string {
	return strings.NewReplacer(
		"{service}", toLabel(v.Service),
		"{project}", toLabel(v.Project),
		"{container}", toLabel(v.Container),
		"{agent}", toLabel(v.Agent),
		"{domain}", v.Domain,
	).Replace(pattern)
}

// CheckPattern validates a hostname pattern by expanding it with sample
// values.
func CheckPattern(pattern string) error {
	if !strings.Contains(pattern, "{service}") && !strings.Contains(pattern, "{container}") {
		return errors.New("das Muster muss {service} oder {container} enthalten, sonst bekämen alle Container denselben Namen")
	}
	if !strings.HasSuffix(pattern, ".{domain}") {
		return errors.New("das Muster muss auf .{domain} enden")
	}
	sample := ExpandPattern(pattern, PatternVars{
		Service: "service", Project: "project", Container: "container", Agent: "agent", Domain: "example.com",
	})
	if !ValidDNSName(sample) || strings.Count(sample, ".") != 2 {
		return errors.New("das Muster ergibt keinen Hostnamen direkt unter der Basis-Domain (erlaubt ist z. B. {service}-{agent}.{domain})")
	}
	return nil
}

// toLabel converts an arbitrary name into a DNS label: "My_App.v2"
// becomes "my-app-v2".
func toLabel(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-")
	if len(label) > 63 {
		label = strings.Trim(label[:63], "-")
	}
	return label
}

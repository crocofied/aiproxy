package routing

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"lanproxy/api"
	"lanproxy/internal/controller/store"
)

// The container labels lanproxy understands.
const (
	LabelEnable = "proxy.enable" // "true" creates a route
	LabelHost   = "proxy.host"   // hostname, or just the subdomain
	LabelPort   = "proxy.port"   // container port, if several are published
	LabelScheme = "proxy.scheme" // "https" for backends speaking (self-signed) HTTPS
)

// Workload returns a name for "what this container is" that survives
// the container being recreated. A container ID changes with every image
// update, so routes are attached to the compose project and service
// instead, or to the container name for containers without compose.
func Workload(c api.Container) string {
	if c.ComposeService != "" {
		return c.ComposeProject + "/" + c.ComposeService
	}
	return c.Name
}

// DesiredLabelRoutes returns the routes requested through labels by the
// containers of one agent, at most one per workload, sorted by workload.
func DesiredLabelRoutes(agentName string, containers []api.Container, s store.Settings) []store.LabelRoute {
	if s.BaseDomain == "" {
		return nil // setup not finished: no hostname can be built yet
	}
	seen := make(map[string]bool)
	var routes []store.LabelRoute

	for _, c := range containers {
		if !strings.EqualFold(strings.TrimSpace(c.Labels[LabelEnable]), "true") {
			continue
		}
		workload := Workload(c)
		if seen[workload] {
			continue // replicas of one service share a route
		}
		seen[workload] = true
		routes = append(routes, labelRoute(agentName, workload, c, s))
	}
	slices.SortFunc(routes, func(a, b store.LabelRoute) int { return cmp.Compare(a.Workload, b.Workload) })
	return routes
}

func labelRoute(agentName, workload string, c api.Container, s store.Settings) store.LabelRoute {
	route := store.LabelRoute{Workload: workload}

	if host := NormalizeHostname(c.Labels[LabelHost]); host != "" {
		// "proxy.host=wiki" is shorthand for "wiki.<base domain>".
		if !strings.Contains(host, ".") {
			host += "." + s.BaseDomain
		}
		route.Hostname = host
	} else {
		service := c.ComposeService
		if service == "" {
			service = c.Name
		}
		route.Hostname = ExpandPattern(s.HostnamePattern, PatternVars{
			Service: service, Project: c.ComposeProject, Container: c.Name,
			Agent: agentName, Domain: s.BaseDomain,
		})
	}
	if err := CheckHostname(route.Hostname, s); err != nil {
		route.Invalid = err.Error()
		return route
	}

	if raw := strings.TrimSpace(c.Labels[LabelPort]); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			route.Invalid = fmt.Sprintf("%s=%q ist kein gültiger Port", LabelPort, raw)
			return route
		}
		route.ContainerPort = port
	}

	switch scheme := strings.ToLower(strings.TrimSpace(c.Labels[LabelScheme])); scheme {
	case "", "http":
	case "https":
		route.BackendHTTPS = true
	default:
		route.Invalid = fmt.Sprintf("%s=%q: erlaubt sind http und https", LabelScheme, scheme)
	}
	return route
}

package traefik

import (
	"encoding/json"
	"fmt"
	"strconv"

	"lanproxy/internal/controller/routing"
	"lanproxy/internal/controller/store"
)

const (
	// ResolverName is the name of the certificate resolver; the static
	// and the dynamic configuration must agree on it.
	ResolverName = "letsencrypt"

	// letsEncryptStaging issues untrusted test certificates with much
	// higher rate limits. An empty CA server means production.
	letsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"

	entryPointHTTP  = "web"
	entryPointHTTPS = "websecure"

	// skipVerifyTransport is used for backends with self-signed HTTPS.
	skipVerifyTransport = "skip-verify"
)

// obj keeps the nested configuration below readable.
type obj = map[string]any

// StaticConfig renders Traefik's static configuration.
//
// The output is JSON. Every JSON document is also valid YAML, so Traefik
// reads it as a .yml file, and we need no YAML library.
func StaticConfig(s store.Settings, p Paths, providerURL string) ([]byte, error) {
	cfg := obj{
		"global": obj{"checkNewVersion": false, "sendAnonymousUsage": false},
		"entryPoints": obj{
			// Port 80 only redirects to HTTPS.
			entryPointHTTP: obj{
				"address": ":80",
				"http": obj{"redirections": obj{"entryPoint": obj{
					"to": entryPointHTTPS, "scheme": "https",
				}}},
			},
			entryPointHTTPS: obj{"address": ":443"},
		},
		"providers": obj{
			"http": obj{"endpoint": providerURL, "pollInterval": "5s"},
		},
		"log": obj{
			"level": "INFO", "format": "json", "filePath": p.LogFile,
			"maxSize": 5, "maxBackups": 2, // megabytes, files
		},
	}

	// Without domain and DNS provider (before the setup) Traefik runs
	// without a certificate resolver.
	if s.BaseDomain != "" && s.DNSProvider != "" {
		challenge := obj{"provider": s.DNSProvider}
		if s.SkipPropagationCheck {
			challenge["propagation"] = obj{
				"disableChecks":     true,
				"delayBeforeChecks": strconv.Itoa(s.PropagationDelaySeconds) + "s",
			}
		} else if len(s.DNSResolvers) > 0 {
			// Public resolvers: a local DNS server with a wildcard
			// rewrite would never show the challenge record.
			challenge["resolvers"] = s.DNSResolvers
		}
		acme := obj{
			"email":        s.ACMEEmail,
			"storage":      p.ACMEFile(s.Staging),
			"dnsChallenge": challenge,
		}
		if s.Staging {
			acme["caServer"] = letsEncryptStaging
		}
		cfg["certificatesResolvers"] = obj{ResolverName: obj{"acme": acme}}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// Dynamic is the document Traefik's HTTP provider polls. Only the parts
// of Traefik's dynamic configuration that lanproxy uses are modelled.
type Dynamic struct {
	HTTP HTTP `json:"http"`
}

// HTTP is the "http" section of the dynamic configuration.
type HTTP struct {
	Routers           map[string]Router          `json:"routers"`
	Services          map[string]Service         `json:"services"`
	ServersTransports map[string]ServerTransport `json:"serversTransports,omitempty"`
}

// Router matches requests and hands them to a service.
type Router struct {
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints"`
	TLS         TLS      `json:"tls"`
}

// TLS makes a router terminate HTTPS.
type TLS struct {
	CertResolver string      `json:"certResolver,omitempty"`
	Domains      []TLSDomain `json:"domains,omitempty"`
}

// TLSDomain names the certificate to request.
type TLSDomain struct {
	Main string   `json:"main"`
	SANs []string `json:"sans,omitempty"`
}

// Service is a backend.
type Service struct {
	LoadBalancer LoadBalancer `json:"loadBalancer"`
}

// LoadBalancer distributes requests over one or more servers.
type LoadBalancer struct {
	Servers          []Server `json:"servers"`
	ServersTransport string   `json:"serversTransport,omitempty"`
}

// Server is one backend address.
type Server struct {
	URL string `json:"url"`
}

// ServerTransport configures how Traefik connects to backends.
type ServerTransport struct {
	InsecureSkipVerify bool `json:"insecureSkipVerify"`
}

// DynamicConfig builds the dynamic configuration: one router for the
// web interface plus one per active route. uiBackend is the address of
// the controller's own web server.
func DynamicConfig(s store.Settings, views []routing.View, uiBackend string) Dynamic {
	cfg := Dynamic{HTTP: HTTP{
		Routers:  map[string]Router{},
		Services: map[string]Service{},
	}}
	if s.UIHost() == "" {
		return cfg // setup not finished: nothing to route yet
	}

	// Every router asks for the same wildcard certificate, so Traefik
	// obtains exactly one certificate, whatever the number of routes.
	tls := TLS{}
	if s.DNSProvider != "" {
		tls = TLS{
			CertResolver: ResolverName,
			Domains:      []TLSDomain{{Main: s.BaseDomain, SANs: []string{"*." + s.BaseDomain}}},
		}
	}
	add := func(name, host string, service Service) {
		cfg.HTTP.Routers[name] = Router{
			// Hostnames are validated (routing.ValidDNSName) and cannot
			// contain a backtick, so no escaping is needed here.
			Rule:        "Host(`" + host + "`)",
			Service:     name,
			EntryPoints: []string{entryPointHTTPS},
			TLS:         tls,
		}
		cfg.HTTP.Services[name] = service
	}

	add("lanproxy-ui", s.UIHost(), Service{LoadBalancer: LoadBalancer{Servers: []Server{{URL: uiBackend}}}})

	for _, v := range views {
		if v.Status != routing.StatusActive {
			continue
		}
		lb := LoadBalancer{}
		for _, url := range v.Servers {
			lb.Servers = append(lb.Servers, Server{URL: url})
		}
		if v.BackendHTTPS {
			lb.ServersTransport = skipVerifyTransport
			cfg.HTTP.ServersTransports = map[string]ServerTransport{
				skipVerifyTransport: {InsecureSkipVerify: true},
			}
		}
		add(fmt.Sprintf("route-%d", v.ID), v.Hostname, Service{LoadBalancer: lb})
	}
	return cfg
}

#!/bin/sh
# End-to-end test with three Docker-in-Docker hosts.
#
#   ./test/e2e/run.sh          run the test, then remove everything
#   ./test/e2e/run.sh --keep   leave the environment running to explore:
#                              http://localhost:18080  (password below)
#
# Needs: docker with compose, curl. Internet access to pull images.
set -eu
cd "$(dirname "$0")"

KEEP=false
[ "${1:-}" = "--keep" ] && KEEP=true

# Defaults; test/e2e/.env overrides them (see .env.example).
E2E_BASE_DOMAIN=lab.test
E2E_ACME_EMAIL=admin@lab.test
E2E_DNS_PROVIDER=cloudflare
E2E_DNS_ENV='{"CF_DNS_API_TOKEN":"dummy-token-for-testing"}'
# shellcheck disable=SC1091
[ -f .env ] && . ./.env

PASSWORD=e2e-test-password
API=http://127.0.0.1:18080
CONTROLLER_IN_LAB=http://172.30.0.10:8080
JAR=$(mktemp)
FAILED=0

cleanup() {
    rm -f "$JAR"
    if [ "$KEEP" = false ]; then
        docker compose down -v --remove-orphans >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT

say() { printf '\n== %s\n' "$*"; }
pass() { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; FAILED=$((FAILED + 1)); }

# api METHOD PATH [JSON]  -> prints the response body
api() {
    if [ $# -ge 3 ]; then
        curl -sS -b "$JAR" -c "$JAR" -X "$1" -H 'Content-Type: application/json' -d "$3" "$API$2"
    else
        curl -sS -b "$JAR" -c "$JAR" -X "$1" "$API$2"
    fi
}

# in_host N COMMAND...  -> runs a docker command inside host N
in_host() {
    n=$1
    shift
    docker compose exec -T "host$n" docker "$@"
}

# site NAME -> HTTP status and body of https://NAME.<domain> via Traefik.
# -k: with a made-up domain Traefik has only its self-signed certificate.
site() {
    curl -sk -m 10 -w '\n%{http_code}' --resolve "$1.$E2E_BASE_DOMAIN:18443:127.0.0.1" \
        "https://$1.$E2E_BASE_DOMAIN:18443/" || true
}

# expect_site NAME STATUS [TEXT] -> retries for up to 60 s
expect_site() {
    i=0
    while [ "$i" -lt 30 ]; do
        out=$(site "$1")
        status=$(printf '%s' "$out" | tail -n 1)
        if [ "$status" = "$2" ] && { [ -z "${3:-}" ] || printf '%s' "$out" | grep -q "$3"; }; then
            pass "https://$1.$E2E_BASE_DOMAIN answers $2"
            return 0
        fi
        i=$((i + 1))
        sleep 2
    done
    fail "https://$1.$E2E_BASE_DOMAIN: expected $2, got $status"
}

# expect_route HOSTNAME STATUS -> checks the route list of the API
expect_route() {
    if api GET /api/routes | tr '{' '\n' | grep "\"hostname\":\"$1\"" | grep -q "\"status\":\"$2\""; then
        pass "route $1 has status $2"
    else
        fail "route $1 does not have status $2"
    fi
}

say "Building images and starting the environment"
docker build -q -f ../../deploy/agent/Dockerfile -t lanproxy-agent:e2e ../.. >/dev/null
docker compose up -d --build --wait lanproxy
docker compose up -d host1 host2 host3

say "Setup through the API (what the wizard does)"
api POST /api/setup "{
  \"password\": \"$PASSWORD\",
  \"base_domain\": \"$E2E_BASE_DOMAIN\",
  \"ui_subdomain\": \"proxy\",
  \"acme_email\": \"$E2E_ACME_EMAIL\",
  \"dns_provider\": \"$E2E_DNS_PROVIDER\",
  \"dns_env\": $E2E_DNS_ENV,
  \"staging\": true,
  \"dns_resolvers\": [\"1.1.1.1:53\", \"9.9.9.9:53\"],
  \"hostname_pattern\": \"{service}.{domain}\",
  \"agent_timeout_seconds\": 30
}" | grep -q ui_host && pass "setup completed" || fail "setup failed"

say "Starting an agent and sample containers on each host"
for n in 1 2 3; do
    i=0
    until in_host "$n" info >/dev/null 2>&1; do
        i=$((i + 1))
        [ "$i" -gt 30 ] && { fail "docker in host$n did not start"; exit 1; }
        sleep 1
    done
    docker save lanproxy-agent:e2e | in_host "$n" load >/dev/null

    token=$(api POST /api/agents "{\"name\": \"host$n\"}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
    [ -n "$token" ] || { fail "no token for host$n"; exit 1; }

    # Host networking inside the DinD container = the "host's" network,
    # so the agent detects 172.30.0.1N as its address.
    in_host "$n" run -d --name agent --network host \
        -e LANPROXY_CONTROLLER_URL="$CONTROLLER_IN_LAB" \
        -e LANPROXY_AGENT_TOKEN="$token" \
        -e LANPROXY_HEARTBEAT_INTERVAL=5s \
        -v /var/run/docker.sock:/var/run/docker.sock:ro \
        lanproxy-agent:e2e >/dev/null

    # A labelled container per host: https://whoamiN.<domain>
    in_host "$n" run -d --name "whoami$n" -p 8080:80 \
        -l proxy.enable=true -l "proxy.host=whoami$n" traefik/whoami >/dev/null
    pass "host$n: agent and whoami$n started"
done
# host2: a container without labels (appears as "detected, not approved").
in_host 2 run -d --name plain -p 8081:80 traefik/whoami >/dev/null
# host3: a container that wants a hostname host1 already owns.
in_host 3 run -d --name clash -p 8082:80 -l proxy.enable=true -l proxy.host=whoami1 traefik/whoami >/dev/null

say "Label routes on all three hosts"
for n in 1 2 3; do
    expect_site "whoami$n" 200 "Hostname:"
done
expect_site proxy 200 "lanproxy" # the web interface itself

say "Conflicts and unlabelled containers"
expect_route "whoami1.$E2E_BASE_DOMAIN" conflict
if api GET /api/containers | tr '{' '\n' | grep '"workload":"plain"' | grep -q '"routed":false'; then
    pass "unlabelled container is listed as not approved"
else
    fail "unlabelled container is missing from the container list"
fi

say "Approving the unlabelled container"
api POST /api/routes '{"source":"ui","hostname":"plain","agent_id":2,"workload":"plain","container_port":80}' >/dev/null
expect_site plain 200 "Hostname:"

say "Manual route (to host1's whoami by IP and port)"
api POST /api/routes '{"source":"manual","hostname":"manual","target_host":"172.30.0.11","target_port":8080}' >/dev/null
expect_site manual 200 "Hostname:"

say "Docker events: a stopped container loses its route at once"
in_host 1 stop whoami1 >/dev/null
expect_site whoami1 200 "Hostname:" # now served by host3's parked route
expect_route "whoami1.$E2E_BASE_DOMAIN" active

say "Agent timeout: host2's agent stops, its routes are switched off"
in_host 2 stop agent >/dev/null
expect_site whoami2 404
expect_route "whoami2.$E2E_BASE_DOMAIN" agent_offline

say "Certificate status"
api GET /api/certificate
echo

say "Result"
if [ "$FAILED" -eq 0 ]; then
    echo "All checks passed."
else
    echo "$FAILED check(s) failed. Logs: docker compose -p lanproxy-e2e logs lanproxy"
fi
if [ "$KEEP" = true ]; then
    echo "Environment left running: $API (password: $PASSWORD)"
    echo "Remove it with: docker compose -p lanproxy-e2e down -v"
fi
exit "$FAILED"

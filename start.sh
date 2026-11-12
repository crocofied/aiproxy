#!/bin/sh
# Starts the lanproxy main node with Docker Compose (Linux, macOS).
#   ./start.sh          build and start
#   ./start.sh stop     stop
#   ./start.sh logs     follow the logs
#   ./start.sh update   git pull, rebuild, restart
set -eu
cd "$(dirname "$0")"

if ! docker compose version >/dev/null 2>&1; then
    echo "Docker with the Compose plugin is required: https://docs.docker.com/engine/install/" >&2
    exit 1
fi

# Optional settings; see .env.example.
[ -f .env ] || cp .env.example .env
port=$(sed -n 's/^LANPROXY_UI_PORT=//p' .env | tail -n 1)
port=${port:-8080}

case "${1:-start}" in
    stop)
        docker compose down
        exit 0 ;;
    logs)
        exec docker compose logs -f ;;
    update)
        git pull --ff-only ;;
    start) ;;
    *)
        echo "usage: $0 [start|stop|logs|update]" >&2
        exit 2 ;;
esac

docker compose up -d --build

printf 'Waiting for lanproxy to start'
i=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' lanproxy 2>/dev/null)" = "healthy" ]; do
    i=$((i + 1))
    if [ "$i" -gt 60 ]; then
        printf '\nlanproxy did not become healthy. Check: docker compose logs\n' >&2
        exit 1
    fi
    printf '.'
    sleep 2
done
printf '\n\nlanproxy is running. Open the web interface:\n\n'
echo "  http://localhost:$port"
# The addresses of this machine in the local network.
if command -v hostname >/dev/null 2>&1; then
    for ip in $(hostname -I 2>/dev/null || true); do
        case "$ip" in
            *:* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) ;; # IPv6 and Docker bridges
            *) echo "  http://$ip:$port" ;;
        esac
    done
fi
echo

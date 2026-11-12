# lanproxy

Reverse-Proxy-Verwaltung für das Homelab: Dienste im lokalen Netz unter eigenen Subdomains mit gültigem HTTPS erreichbar machen, mit automatischer Erkennung von Docker-Containern auf beliebig vielen Hosts.

- **Nur lokales Netz.** Kein Tunnel, kein VPN, keine Portfreigabe. Der Server muss aus dem Internet nicht erreichbar sein.
- **Gültiges HTTPS.** Ein Let's-Encrypt-Wildcard-Zertifikat für die eigene Domain, ausgestellt per DNS-01-Challenge über die API des DNS-Anbieters.
- **Traefik v3** ist der Proxy. lanproxy erzeugt nur dessen Konfiguration.
- **Ein Container** auf dem Hauptknoten (Traefik + Controller + Weboberfläche), dazu ein kleiner **Agent** pro Docker-Host.

```
Agent (Host A..N) ──meldet Container──▶ Controller ◀──holt Config── Traefik :80/:443
  liest Docker-Socket                    SQLite, UI                    │
                                                                       └──▶ Host-IP:Host-Port der Container
```

## Schnellstart

Voraussetzungen: Docker mit Compose-Plugin (ab 2.24), eine eigene Domain bei einem DNS-Anbieter mit API, freie Ports 80 und 443 auf dem Hauptknoten.

**Linux / macOS**

```bash
git clone https://github.com/crocofied/aiproxy.git
```

```bash
cd aiproxy && ./start.sh
```

**Windows** (Docker Desktop): Repository klonen und `start.bat` doppelklicken, oder in PowerShell:

```powershell
.\start.ps1
```

Die Skripte bauen das Image, starten den Container, warten, bis er läuft, und zeigen die Adresse der Oberfläche an (`http://HOST-IP:8080`). Weitere Aufrufe: `stop`, `logs`, `update`. Ohne Skript geht es genauso:

```bash
docker compose up -d --build
```

Danach die Oberfläche öffnen und dem **Einrichtungsassistenten** folgen:

1. Admin-Passwort
2. Basis-Domain (z. B. `home.example.com`), Subdomain für die Oberfläche, E-Mail-Adresse für Let's Encrypt
3. DNS-Anbieter und Zugangsdaten, Staging ja/nein
4. Lokalen DNS-Eintrag anlegen (siehe unten)

Tipp: Beim ersten Mal **Staging** aktivieren. Let's Encrypt begrenzt echte Zertifikate pro Domain und Woche; im Staging lässt sich die Einrichtung gefahrlos testen. Wenn im Zertifikatsstatus ein (nicht vertrauenswürdiges) Staging-Zertifikat erscheint, in den Einstellungen auf Produktion umschalten.

## DNS-Token mit minimalen Rechten (Beispiel Cloudflare)

lanproxy braucht nur das Recht, DNS-Einträge **einer** Zone zu bearbeiten.

1. Cloudflare-Dashboard → Profil → **API-Token** → **Token erstellen** → Vorlage **„Zonen-DNS bearbeiten“**
2. Berechtigungen: **Zone → DNS → Bearbeiten** (keine weiteren)
3. Zonenressourcen: **Einschließen → Bestimmte Zone →** die Zone der Basis-Domain
4. Token erstellen und im Assistenten eintragen

Keinen „Global API Key“ verwenden. Der Token wird ausschließlich in `/data/secrets/dns-env/` mit Dateirechten 600 gespeichert, nie angezeigt und nie geloggt.

**Andere Anbieter:** Im Assistenten „Anderer Anbieter“ wählen, den *Provider Code* aus der [Traefik-Dokumentation](https://doc.traefik.io/traefik/reference/install-configuration/tls/certificate-resolvers/acme/#providers) eintragen und die dort genannten Umgebungsvariablen als Name/Wert-Paare angeben.

## Lokaler DNS-Eintrag

Der lokale DNS-Server muss alle Namen unter der Basis-Domain auf die IP des Hauptknotens zeigen lassen (Wildcard-Rewrite). Beim öffentlichen DNS-Anbieter ist dafür **kein** Eintrag nötig.

```
*.home.example.com  →  192.168.1.10
home.example.com    →  192.168.1.10
```

| DNS-Server | Vorgehen |
|---|---|
| **AdGuard Home** | Filter → DNS-Umschreibungen → `*.home.example.com` → IP des Hauptknotens, dazu ein zweiter Eintrag für `home.example.com` |
| **Pi-hole** | Datei `/etc/dnsmasq.d/99-lanproxy.conf` mit `address=/home.example.com/192.168.1.10`, dann `pihole restartdns`. Bei Pi-hole 6 zusätzlich `misc.etc_dnsmasq_d` aktivieren. |
| **Router** | Viele Router kennen keine Wildcards. Dann jeden Hostnamen einzeln eintragen oder einen der DNS-Server oben per DHCP verteilen. |

**DNS-Rebind-Schutz:** Router wie die Fritzbox verwerfen DNS-Antworten, die auf private IP-Adressen zeigen. In der Fritzbox unter Heimnetz → Netzwerk → Netzwerkeinstellungen → DNS-Rebind-Schutz die Basis-Domain als Ausnahme eintragen.

Die Oberfläche zeigt diese Anleitung mit den eigenen Werten an (Assistent, Einstellungen, Hilfe).

## Agents

Ein Agent pro Docker-Host, auch auf dem Hauptknoten selbst. Er liest den Docker-Socket, meldet laufende Container mit veröffentlichten Ports an den Controller und öffnet selbst keinen Port. Über ihn läuft kein Datenverkehr.

1. Oberfläche → **Agents → Agent hinzufügen**, Namen vergeben
2. Die angezeigte `docker-compose.yml` auf den Host kopieren (der Token wird nur einmal angezeigt)
3. `docker compose up -d`

Das Agent-Image wird dabei direkt aus diesem Repository gebaut. Einstellungen des Agents:

| Variable | Bedeutung | Standard |
|---|---|---|
| `LANPROXY_CONTROLLER_URL` | Adresse des Controllers | Pflicht |
| `LANPROXY_AGENT_TOKEN` oder `LANPROXY_AGENT_TOKEN_FILE` | Token aus der Oberfläche | Pflicht |
| `LANPROXY_HOST_IP` | IP des Hosts, unter der seine Ports erreichbar sind | automatisch |
| `LANPROXY_HEARTBEAT_INTERVAL` | Abstand der regelmäßigen Meldungen | `30s` |
| `LANPROXY_DOCKER_SOCKET` | Pfad des Docker-Sockets | `/var/run/docker.sock` |
| `LANPROXY_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |

Hinweise:

- Die automatische Erkennung der Host-IP braucht `network_mode: host`. Unter **Docker Desktop (Windows, macOS)** liefert sie nicht die LAN-Adresse; dort `LANPROXY_HOST_IP` setzen.
- `:ro` am Docker-Socket macht die Docker-API nicht schreibgeschützt. Der Agent sendet nur zwei lesende Anfragen (`internal/agent/docker`). Wer mehr Abschottung will, schaltet einen docker-socket-proxy davor.
- Meldet sich ein Agent länger als den Agent-Timeout (Standard 5 Minuten) nicht, werden seine Routen abgeschaltet, bis er wieder da ist.

## Routen

| Quelle | Entstehung |
|---|---|
| **Label** | Container mit `proxy.enable=true` |
| **UI** | Erkannter Container ohne Label, unter „Container“ per Klick freigegeben |
| **Manuell** | Beliebige IP:Port, z. B. NAS, Hypervisor, Smart-Home-Zentrale; optional „Backend nutzt selbstsigniertes HTTPS“ |

### Labels

| Label | Bedeutung |
|---|---|
| `proxy.enable=true` | Route anlegen. Hostname nach Muster, Standard `<compose-service>.<basis-domain>` |
| `proxy.host=wiki` | Hostname überschreiben (Subdomain oder voller Name) |
| `proxy.port=80` | Container-Port wählen, wenn mehrere veröffentlicht sind |
| `proxy.scheme=https` | Container spricht selbst HTTPS (selbstsigniert, wird nicht geprüft) |

```yaml
services:
  wiki:
    image: example/wiki
    ports:
      - "8080:3000"          # der Port muss veröffentlicht sein
    labels:
      proxy.enable: "true"   # → https://wiki.home.example.com
```

Regeln:

- Container müssen ihre Ports **veröffentlichen** (`ports:`). Traefik verbindet sich mit Host-IP:Host-Port. Ports, die nur an `127.0.0.1` gebunden sind, und Container mit `network_mode: host` sind nicht erreichbar bzw. werden nicht erkannt (dann manuelle Route).
- Hostnamen müssen direkt unter der Basis-Domain liegen; `a.b.home.example.com` deckt das Wildcard-Zertifikat nicht ab.
- **Konflikte:** Jeder Hostname gehört genau einer Route. Wer zuerst kommt, behält ihn. Spätere Routen werden abgelehnt (UI/manuell) oder als „Konflikt“ angezeigt (Label) und nie aktiv, solange der Name vergeben ist.

## Zertifikate

- Wildcard-Zertifikat für `*.basis-domain` plus die Basis-Domain, ausschließlich per **DNS-01**.
- Die Sichtbarkeit des Challenge-Eintrags wird über **öffentliche Resolver** geprüft (Standard `1.1.1.1` und `9.9.9.9`, einstellbar), weil lokale DNS-Server die Domain oft umschreiben. Blockiert das Netz ausgehendes DNS, lässt sich die Prüfung durch eine Wartezeit ersetzen.
- Traefik erneuert selbständig. Zertifikate liegen in `/data/acme/`, getrennt nach Staging und Produktion.
- Die Oberfläche zeigt Status, Ablaufdatum und letzte Erneuerung und warnt auf jeder Seite, wenn eine Ausstellung oder Erneuerung fehlschlägt oder weniger als 14 Tage verbleiben.

Der Hauptknoten braucht **ausgehend** Zugriff auf Let's Encrypt, die API des DNS-Anbieters und (für die Prüfung) Port 53 der öffentlichen Resolver.

## Konfiguration des Hauptknotens

Alle Angaben des Assistenten lassen sich per Umgebungsvariable vorgeben (Datei `.env`, Vorlage `.env.example`); der Assistent ist dann vorausgefüllt. Passwort und DNS-Zugangsdaten aus der Umgebung werden übernommen, wenn die Felder leer bleiben, aber nie angezeigt.

| Variable | Bedeutung | Standard |
|---|---|---|
| `LANPROXY_UI_PORT` | Host-Port für den Direktzugriff auf die Oberfläche | `8080` |
| `LANPROXY_BASE_DOMAIN`, `LANPROXY_UI_SUBDOMAIN`, `LANPROXY_ACME_EMAIL` | Vorgaben für den Assistenten | – |
| `LANPROXY_DNS_PROVIDER`, `LANPROXY_ACME_STAGING`, `LANPROXY_DNS_RESOLVERS` | Vorgaben für den Assistenten | – |
| `LANPROXY_ADMIN_PASSWORD` | Vorgabe für das Admin-Passwort | – |
| `LANPROXY_DNS_ENV_<NAME>` | Variable `NAME` für den DNS-Anbieter, z. B. `LANPROXY_DNS_ENV_CF_DNS_API_TOKEN` | – |
| `LANPROXY_AGENT_IMAGE`, `LANPROXY_AGENT_BUILD_CONTEXT` | Image und Build-Quelle in der generierten Agent-Compose-Datei | siehe `docker-compose.yml` |
| `LANPROXY_LISTEN`, `LANPROXY_PROVIDER_LISTEN`, `LANPROXY_DATA_DIR`, `LANPROXY_LOG_LEVEL` | Interna des Controllers | `:8080`, `127.0.0.1:8081`, `/data`, `info` |

Alles Persistente liegt im Volume `/data`:

```
/data/lanproxy.db            SQLite: Einstellungen, Agents, Routen (600)
/data/secrets/dns-env/       Zugangsdaten des DNS-Anbieters, eine Datei pro Variable (600)
/data/acme/                  Zertifikate und Let's-Encrypt-Konto (von Traefik)
/data/traefik/traefik.yml    generierte statische Traefik-Konfiguration
/data/logs/traefik.log       Traefik-Log (rotiert)
```

Änderungen an Domain, Anbieter, Zugangsdaten oder Staging schreiben die statische Traefik-Konfiguration neu und starten **nur den Traefik-Prozess** im Container neu. Routen ändern sich ohne Neustart.

## Sicherheit

Ausgelegt für ein vertrauenswürdiges lokales Netz.

- Ein Admin-Benutzer, Passwort mit bcrypt gehasht. Sitzungen als `HttpOnly`/`SameSite=Strict`-Cookie; in der Datenbank steht nur ein Hash.
- Agent-Token: 256 Bit Zufall, einmalig sichtbar, im Controller nur als SHA-256-Hash gespeichert.
- Secrets erscheinen weder im Log noch in API-Antworten (durch Tests abgesichert).
- Bis zum Abschluss des Assistenten kann jeder im Netz die Einrichtung durchführen. Danach ist sie gesperrt.
- Der Direktzugriff über `http://HOST-IP:8080` ist unverschlüsselt und bleibt als Notzugang bestehen. Wer ihn nicht möchte, entfernt die Port-Zeile aus `docker-compose.yml`. Agents, die über diese Adresse melden, übertragen ihren Token im Klartext; mit gültigem Zertifikat besser `https://<ui-subdomain>.<basis-domain>` verwenden.
- Der Container läuft als root (Ports 80/443, Neustart von Traefik).

## Nicht enthalten

Tunnel/VPN, öffentliche Freigaben, SSO, Zugriffskontrolle pro Route, TCP/UDP-Routing, mehrere Benutzer, Hochverfügbarkeit.

## Entwicklung

Voraussetzungen: Go 1.24, Node 22, golangci-lint v2.

```
api/                    gemeinsame Typen Agent ↔ Controller
cmd/agent, cmd/controller
internal/agent/         docker (Socket-Client), hostip, reporter, config
internal/controller/    store (SQLite), auth, routing, traefik, certs, httpapi, ui (go:embed)
web/                    SvelteKit-Oberfläche (adapter-static)
deploy/                 Dockerfiles, s6-Dienste, Agent-Compose
test/e2e/               Testumgebung mit drei Docker-in-Docker-Hosts
```

| Befehl | Wirkung |
|---|---|
| `make build` | Agent und Controller (inkl. Oberfläche) nach `bin/` |
| `make test` | Go-Tests mit Race-Detector, Typprüfung der Oberfläche |
| `make lint` | golangci-lint |
| `make images` | beide Docker-Images |
| `make up` / `make down` | Hauptknoten starten / stoppen |
| `make e2e` | Ende-zu-Ende-Test (siehe unten) |

Lokal ohne Docker: `make build`, dann `LANPROXY_DATA_DIR=./data ./bin/controller` und die Oberfläche unter `http://localhost:8080` öffnen. Ohne Restart-Kommando wird Traefik nicht gesteuert; die generierte Konfiguration liegt in `./data/traefik/`.

Abhängigkeiten (bewusst wenige):

| Abhängigkeit | Grund |
|---|---|
| `modernc.org/sqlite` | SQLite ohne CGO, damit statische Binaries möglich sind |
| `golang.org/x/crypto/bcrypt` | Passwort-Hash; braucht im Gegensatz zu Argon2 kaum Speicher |
| SvelteKit, Vite, TypeScript | nur zur Bauzeit der Oberfläche |

Der Agent hat keine externen Abhängigkeiten: Die zwei benötigten Docker-API-Aufrufe sind direkt mit `net/http` über den Unix-Socket umgesetzt. Router ist der `http.ServeMux` der Standardbibliothek. Die Traefik-Konfiguration wird als JSON geschrieben (gültiges YAML), daher keine YAML-Bibliothek.

### Testumgebung

`make e2e` startet den Hauptknoten und drei „Hosts“ als Docker-in-Docker-Container mit eigenen IP-Adressen, richtet lanproxy über die API ein, startet auf jedem Host einen Agent und Beispiel-Container und prüft Label-Routen, Konflikte, Freigabe per UI, manuelle Routen, Docker-Events und den Agent-Timeout.

```bash
./test/e2e/run.sh --keep
```

`--keep` lässt die Umgebung zum Ausprobieren laufen (`http://localhost:18080`).

Ohne weitere Angaben benutzt der Test eine erfundene Domain: Alles außer der Zertifikatsausstellung wird geprüft, Traefik liefert sein selbstsigniertes Standardzertifikat. Für einen Test **mit Let's-Encrypt-Staging** `test/e2e/.env.example` nach `test/e2e/.env` kopieren und eine eigene Domain samt DNS-Token eintragen.

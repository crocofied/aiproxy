<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import DnsHints from '#lib/DnsHints.svelte';
	import type { SettingsResponse } from '#lib/types.ts';

	let domain = $state('home.example.com');
	let pattern = $state('{service}.{domain}');

	onMount(async () => {
		try {
			const s = await api<SettingsResponse>('GET', '/api/settings');
			domain = s.base_domain;
			pattern = s.hostname_pattern;
		} catch {
			// the examples then use a placeholder domain
		}
	});
</script>

<h1>Hilfe</h1>

<div class="card">
	<h2>So funktioniert es</h2>
	<ol>
		<li>Auf jedem Docker-Host läuft ein <strong>Agent</strong>. Er liest, welche Container laufen und welche Ports sie veröffentlichen, und meldet das an diesen Controller.</li>
		<li>Der Controller macht daraus <strong>Routen</strong>: Hostname → Host-IP:Host-Port.</li>
		<li><strong>Traefik</strong> (im selben Container wie der Controller) nimmt Anfragen auf Port 443 an und leitet sie direkt an Host-IP:Host-Port weiter. Über den Agent läuft kein Datenverkehr.</li>
	</ol>
</div>

<div class="card">
	<h2>Agents einbinden</h2>
	<ol>
		<li>Unter <a href="/agents">Agents</a> auf „Agent hinzufügen“ klicken und einen Namen vergeben.</li>
		<li>Die angezeigte <code>docker-compose.yml</code> auf den Docker-Host kopieren. Der Token darin wird nur einmal angezeigt.</li>
		<li>Dort <code>docker compose up -d</code> ausführen. Der Agent erscheint nach wenigen Sekunden als online.</li>
	</ol>
	<p>
		Der Agent meldet Änderungen sofort und sonst alle 30 Sekunden (<code>LANPROXY_HEARTBEAT_INTERVAL</code>).
		Bleibt er länger aus als der Agent-Timeout (Standard 5 Minuten), werden seine Routen abgeschaltet,
		bis er sich wieder meldet.
	</p>
	<p>
		Der Agent verwendet <code>network_mode: host</code>, um die IP-Adresse des Hosts zu erkennen. Unter
		Docker Desktop (Windows, macOS) funktioniert das nicht; dort die Adresse mit
		<code>LANPROXY_HOST_IP</code> vorgeben.
	</p>
</div>

<div class="card">
	<h2>Labels</h2>
	<div class="table-wrap">
		<table>
			<thead><tr><th>Label</th><th>Bedeutung</th></tr></thead>
			<tbody>
				<tr>
					<td><code>proxy.enable=true</code></td>
					<td>Legt automatisch eine Route an. Ohne weitere Labels ergibt sich der Hostname aus dem Muster <code>{pattern}</code>.</td>
				</tr>
				<tr>
					<td><code>proxy.host=…</code></td>
					<td>Überschreibt den Hostnamen. Entweder nur die Subdomain (<code>wiki</code>) oder der volle Name (<code>wiki.{domain}</code>).</td>
				</tr>
				<tr>
					<td><code>proxy.port=…</code></td>
					<td>Der <strong>Container-Port</strong>, falls der Container mehrere Ports veröffentlicht.</td>
				</tr>
				<tr>
					<td><code>proxy.scheme=https</code></td>
					<td>Für Container, die selbst HTTPS mit selbstsigniertem Zertifikat sprechen. Das Zertifikat des Containers wird nicht geprüft.</td>
				</tr>
			</tbody>
		</table>
	</div>

	<h3>Beispiel: ein Port, Standardname</h3>
	<pre>services:
  wiki:
    image: example/wiki
    ports:
      - "8080:3000"
    labels:
      proxy.enable: "true"
# erreichbar unter https://wiki.{domain}</pre>

	<h3>Beispiel: eigener Name, mehrere Ports</h3>
	<pre>services:
  app:
    image: example/app
    ports:
      - "8081:80"     # Weboberfläche
      - "9100:9100"   # Metriken
    labels:
      proxy.enable: "true"
      proxy.host: "dashboard"
      proxy.port: "80"
# erreichbar unter https://dashboard.{domain}</pre>

	<h3>Beispiel: Container mit eigenem HTTPS</h3>
	<pre>services:
  portainer:
    image: portainer/portainer-ce
    ports:
      - "9443:9443"
    labels:
      proxy.enable: "true"
      proxy.scheme: "https"</pre>

	<h3>Regeln für Hostnamen</h3>
	<ul>
		<li>Ein Hostname muss direkt unter der Basis-Domain liegen (<code>name.{domain}</code>). Tiefere Ebenen wie <code>a.b.{domain}</code> deckt das Wildcard-Zertifikat nicht ab.</li>
		<li>Jeder Hostname gehört genau einer Route. Wer ihn zuerst belegt, behält ihn; spätere Routen mit demselben Namen erscheinen als „Konflikt“ und werden nicht aktiv.</li>
	</ul>
</div>

<div class="card">
	<h2>Ports</h2>
	<ul>
		<li>Container müssen ihren Port <strong>veröffentlichen</strong> (<code>ports:</code> bzw. <code>-p</code>). Traefik verbindet sich mit Host-IP:Host-Port, nicht mit dem Docker-Netz des Containers. <code>expose:</code> allein genügt nicht.</li>
		<li>Ports, die nur an <code>127.0.0.1</code> gebunden sind (<code>127.0.0.1:8080:80</code>), sind vom Proxy aus nicht erreichbar.</li>
		<li>Container mit <code>network_mode: host</code> veröffentlichen keine Ports und werden nicht erkannt. Dafür eine manuelle Route anlegen.</li>
		<li>Geroutet wird nur HTTP/HTTPS über TCP.</li>
		<li>Geräte ohne Docker (NAS, Hypervisor, Smart-Home-Zentrale) bekommen unter <a href="/">Routen</a> eine manuelle Route auf IP:Port.</li>
	</ul>
</div>

<div class="card">
	<h2>Cloudflare-API-Token mit minimalen Rechten</h2>
	<ol>
		<li>Im Cloudflare-Dashboard: Profil → API-Token → „Token erstellen“ → Vorlage „Zonen-DNS bearbeiten“.</li>
		<li>Berechtigung: <strong>Zone → DNS → Bearbeiten</strong>. Weitere Berechtigungen sind nicht nötig.</li>
		<li>Zonenressourcen: <strong>Einschließen → Bestimmte Zone →</strong> die Zone der Basis-Domain.</li>
		<li>Token erstellen und in lanproxy eintragen. Er wird nur in einer Datei mit Rechten 600 gespeichert und nie angezeigt.</li>
	</ol>
</div>

<div class="card">
	<h2>Lokaler DNS-Eintrag</h2>
	<DnsHints baseDomain={domain} />
</div>

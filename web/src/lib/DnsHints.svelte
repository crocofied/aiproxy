<script lang="ts">
	// Explains the one DNS record the local network needs.
	let { baseDomain }: { baseDomain: string } = $props();

	// If the page was opened via IP address, that is the main node's IP.
	const host = window.location.hostname;
	const ip = /^[0-9.]+$/.test(host) || host.includes(':') ? host : 'IP-DES-HAUPTKNOTENS';
	const domain = $derived(baseDomain || 'home.example.com');
</script>

<p>
	Damit Geräte im lokalen Netz die Subdomains finden, muss der <strong>lokale DNS-Server</strong> alle
	Namen unter <code>{domain}</code> auf den Hauptknoten zeigen lassen:
</p>
<pre>*.{domain}  →  {ip}
{domain}    →  {ip}</pre>
<p class="muted small">
	Beim öffentlichen DNS-Anbieter ist dafür kein Eintrag nötig. Er wird nur für die Zertifikatsprüfung
	benutzt.
</p>

<h3>AdGuard Home</h3>
<p>
	Filter → DNS-Umschreibungen → „DNS-Umschreibung hinzufügen“: Domain <code>*.{domain}</code>, Antwort
	<code>{ip}</code>. Für die Basis-Domain selbst einen zweiten Eintrag <code>{domain}</code> anlegen.
</p>

<h3>Pi-hole</h3>
<p>
	Pi-hole kennt in der Oberfläche keine Wildcards. Eine Datei
	<code>/etc/dnsmasq.d/99-lanproxy.conf</code> mit folgender Zeile anlegen und danach
	<code>pihole restartdns</code> ausführen (bei Pi-hole 6 zusätzlich unter Einstellungen → Alle
	Einstellungen → Verschiedenes <code>misc.etc_dnsmasq_d</code> aktivieren):
</p>
<pre>address=/{domain}/{ip}</pre>

<h3>Router-DNS</h3>
<p>
	Viele Router können nur einzelne Namen, keine Wildcards. Dann entweder jeden Hostnamen einzeln
	eintragen oder einen der DNS-Server oben verwenden und ihn im Router per DHCP als DNS-Server
	verteilen.
</p>
<p>
	<strong>DNS-Rebind-Schutz:</strong> Router wie die Fritzbox verwerfen DNS-Antworten, die auf private
	IP-Adressen zeigen. Die Namen lassen sich dann nicht auflösen, obwohl der Eintrag stimmt. In der
	Fritzbox unter Heimnetz → Netzwerk → Netzwerkeinstellungen → DNS-Rebind-Schutz die Domain
	<code>{domain}</code> als Ausnahme eintragen.
</p>

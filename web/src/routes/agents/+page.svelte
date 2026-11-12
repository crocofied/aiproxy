<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ago, errorText, poll } from '#lib/api.ts';
	import { cert } from '#lib/state.svelte.ts';
	import type { Agent, AgentCreated, SettingsResponse } from '#lib/types.ts';

	let agents = $state<Agent[] | null>(null);
	let error = $state('');

	let name = $state('');
	let addOpen = $state(false);
	let addError = $state('');

	// Set after creating an agent or renewing a token. The token exists
	// only here, in the memory of this page.
	let created = $state<AgentCreated | null>(null);
	let controllerUrl = $state('');
	let hostIp = $state('');
	let copied = $state(false);

	async function load() {
		try {
			agents = await api<Agent[]>('GET', '/api/agents');
			error = '';
		} catch (e) {
			error = errorText(e);
		}
	}
	onMount(() => poll(load, 5000));

	// Agents should talk HTTPS once a trusted certificate exists; until
	// then the address this page was opened with is the one that works.
	async function defaultControllerUrl(): Promise<string> {
		const c = cert.current;
		if (c && c.state === 'valid' && !c.staging) {
			const settings = await api<SettingsResponse>('GET', '/api/settings');
			return `https://${settings.ui_host}`;
		}
		return window.location.origin;
	}

	async function show(result: AgentCreated) {
		created = result;
		controllerUrl = await defaultControllerUrl();
		hostIp = '';
		copied = false;
		await load();
	}

	async function add(event: SubmitEvent) {
		event.preventDefault();
		addError = '';
		try {
			await show(await api<AgentCreated>('POST', '/api/agents', { name }));
			name = '';
			addOpen = false;
		} catch (e) {
			addError = errorText(e);
		}
	}

	async function renew(agent: Agent) {
		if (!confirm(`Neuen Token für ${agent.name} erzeugen? Der bisherige Token wird sofort ungültig.`)) return;
		try {
			await show(await api<AgentCreated>('POST', `/api/agents/${agent.id}/token`));
		} catch (e) {
			error = errorText(e);
		}
	}

	async function remove(agent: Agent) {
		if (!confirm(`Agent ${agent.name} löschen? Seine Routen werden entfernt.`)) return;
		try {
			await api('DELETE', `/api/agents/${agent.id}`);
			await load();
		} catch (e) {
			error = errorText(e);
		}
	}

	// The compose file for the new agent, ready to copy.
	const compose = $derived.by(() => {
		if (!created) return '';
		const lines = ['services:', '  lanproxy-agent:', `    image: ${created.image}`];
		if (created.build_context) {
			lines.push('    build:', `      context: ${created.build_context}`, '      dockerfile: deploy/agent/Dockerfile');
		}
		lines.push(
			'    container_name: lanproxy-agent',
			'    restart: unless-stopped',
			'    network_mode: host',
			'    environment:',
			`      LANPROXY_CONTROLLER_URL: "${controllerUrl}"`,
			`      LANPROXY_AGENT_TOKEN: "${created.token}"`
		);
		if (hostIp.trim() !== '') lines.push(`      LANPROXY_HOST_IP: "${hostIp.trim()}"`);
		lines.push(
			'    volumes:',
			'      - /var/run/docker.sock:/var/run/docker.sock:ro',
			'    read_only: true',
			'    security_opt:',
			'      - no-new-privileges:true'
		);
		return lines.join('\n') + '\n';
	});

	async function copy() {
		// The clipboard API needs HTTPS or localhost; via http://IP:port
		// the text has to be selected by hand.
		try {
			await navigator.clipboard.writeText(compose);
			copied = true;
		} catch {
			copied = false;
			alert('Kopieren ist über eine unverschlüsselte Verbindung nicht möglich. Bitte den Text markieren und kopieren.');
		}
	}
</script>

<div class="spread">
	<h1>Agents</h1>
	<button class="primary" onclick={() => ((addOpen = !addOpen), (addError = ''))}>Agent hinzufügen</button>
</div>

{#if addOpen}
	<form class="card" onsubmit={add}>
		<h2>Neuer Agent</h2>
		<p class="muted">Ein Agent pro Docker-Host, auch auf dem Hauptknoten selbst.</p>
		{#if addError}<div class="notice bad">{addError}</div>{/if}
		<div class="row">
			<label>
				Name
				<input type="text" bind:value={name} placeholder="z. B. nas oder docker-01" required autocomplete="off" />
			</label>
			<button class="primary fixed" style="margin-top: 1.55rem">Token erzeugen</button>
		</div>
	</form>
{/if}

{#if created}
	<div class="card">
		<div class="spread">
			<h2>Agent „{created.agent.name}“ starten</h2>
			<button onclick={() => (created = null)}>Schließen</button>
		</div>
		<div class="notice warn">
			<strong>Der Token wird nur jetzt angezeigt.</strong> Gespeichert ist nur ein Hash. Geht er verloren,
			lässt sich ein neuer erzeugen.
		</div>
		<div class="row">
			<label>
				Adresse des Controllers aus Sicht des Agents
				<input type="text" bind:value={controllerUrl} autocomplete="off" />
				<span class="hint">
					Über <code>http://IP:Port</code> wird der Token unverschlüsselt im lokalen Netz übertragen. Mit
					gültigem Zertifikat besser die HTTPS-Adresse der Oberfläche verwenden.
				</span>
			</label>
			<label>
				Host-IP (optional)
				<input type="text" bind:value={hostIp} placeholder="automatisch" autocomplete="off" />
				<span class="hint">
					Nur nötig, wenn die automatische Erkennung nicht passt, z. B. bei Docker Desktop unter Windows
					oder macOS.
				</span>
			</label>
		</div>
		<p>
			Auf dem Docker-Host als <code>docker-compose.yml</code> speichern und mit
			<code>docker compose up -d</code> starten:
		</p>
		<pre>{compose}</pre>
		<button onclick={copy}>{copied ? 'Kopiert' : 'In die Zwischenablage kopieren'}</button>
	</div>
{/if}

{#if error}<div class="notice bad">{error}</div>{/if}

<div class="card">
	{#if agents === null}
		<p class="muted">Lädt …</p>
	{:else if agents.length === 0}
		<p class="empty">Noch keine Agents. Ohne Agent werden keine Container erkannt.</p>
	{:else}
		<div class="table-wrap">
			<table>
				<thead>
					<tr><th>Name</th><th>Status</th><th>Letzte Meldung</th><th>Host</th><th>Container</th><th></th></tr>
				</thead>
				<tbody>
					{#each agents as agent (agent.id)}
						<tr>
							<td><strong>{agent.name}</strong></td>
							<td>
								{#if agent.online}
									<span class="badge ok">online</span>
								{:else if agent.last_seen_at}
									<span class="badge warn">offline</span>
								{:else}
									<span class="badge">noch nie gemeldet</span>
								{/if}
							</td>
							<td>{ago(agent.last_seen_at)}</td>
							<td>
								{#if agent.host_ip}
									<code>{agent.host_ip}</code>
									<div class="muted small">
										{agent.hostname}{agent.version ? `, Version ${agent.version}` : ''}
										{#if agent.observed_ip && agent.observed_ip !== agent.host_ip}
											<br />Meldung kam von {agent.observed_ip}
										{/if}
									</div>
								{:else}
									–
								{/if}
							</td>
							<td>{agent.containers}</td>
							<td class="actions">
								<button onclick={() => renew(agent)}>Neuer Token</button>
								<button class="danger" onclick={() => remove(agent)}>Löschen</button>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

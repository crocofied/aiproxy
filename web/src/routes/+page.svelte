<script lang="ts">
	import { onMount } from 'svelte';
	import { api, errorText, poll } from '#lib/api.ts';
	import { statusInfo, sourceLabel, targetText } from '#lib/status.ts';
	import type { Route } from '#lib/types.ts';

	let routes = $state<Route[] | null>(null);
	let error = $state('');

	// The form is used both for new manual routes and for editing.
	const emptyForm = { id: 0, hostname: '', target_host: '', target_port: 80, backend_https: false };
	let form = $state({ ...emptyForm });
	let formOpen = $state(false);
	let formError = $state('');

	async function load() {
		try {
			routes = await api<Route[]>('GET', '/api/routes');
			error = '';
		} catch (e) {
			error = errorText(e);
		}
	}
	onMount(() => poll(load, 5000));

	function edit(route: Route) {
		form = {
			id: route.id,
			hostname: route.hostname,
			target_host: route.target_host ?? '',
			target_port: route.target_port ?? 80,
			backend_https: route.backend_https
		};
		formError = '';
		formOpen = true;
	}

	async function save(event: SubmitEvent) {
		event.preventDefault();
		formError = '';
		const body = { source: 'manual', ...form, id: undefined };
		try {
			if (form.id) await api('PUT', `/api/routes/${form.id}`, body);
			else await api('POST', '/api/routes', body);
			formOpen = false;
			form = { ...emptyForm };
			await load();
		} catch (e) {
			formError = errorText(e);
		}
	}

	async function remove(route: Route) {
		if (!confirm(`Route ${route.hostname} löschen?`)) return;
		try {
			await api('DELETE', `/api/routes/${route.id}`);
			await load();
		} catch (e) {
			error = errorText(e);
		}
	}
</script>

<div class="spread">
	<h1>Routen</h1>
	<button
		class="primary"
		onclick={() => {
			form = { ...emptyForm };
			formError = '';
			formOpen = !formOpen;
		}}
	>
		Manuelle Route hinzufügen
	</button>
</div>

{#if formOpen}
	<form class="card" onsubmit={save}>
		<h2>{form.id ? 'Manuelle Route bearbeiten' : 'Manuelle Route'}</h2>
		<p class="muted">Für Geräte ohne Docker, z. B. NAS, Hypervisor oder Smart-Home-Zentrale.</p>
		{#if formError}<div class="notice bad">{formError}</div>{/if}
		<div class="row">
			<label>
				Hostname
				<input type="text" bind:value={form.hostname} placeholder="nas" required autocomplete="off" />
				<span class="hint">Nur die Subdomain oder der volle Name.</span>
			</label>
			<label>
				Ziel (IP-Adresse oder Hostname)
				<input type="text" bind:value={form.target_host} required autocomplete="off" />
			</label>
			<label>
				Port
				<input type="number" bind:value={form.target_port} min="1" max="65535" required />
			</label>
		</div>
		<label class="check">
			<input type="checkbox" bind:checked={form.backend_https} />
			<span>
				Backend nutzt selbstsigniertes HTTPS
				<span class="hint">Der Proxy spricht HTTPS mit dem Ziel und prüft dessen Zertifikat nicht.</span>
			</span>
		</label>
		<div class="row">
			<button class="primary fixed">Speichern</button>
			<button type="button" class="fixed" onclick={() => (formOpen = false)}>Abbrechen</button>
		</div>
	</form>
{/if}

{#if error}<div class="notice bad">{error}</div>{/if}

<div class="card">
	{#if routes === null}
		<p class="muted">Lädt …</p>
	{:else if routes.length === 0}
		<p class="empty">
			Noch keine Routen. Container mit dem Label <code>proxy.enable=true</code> erscheinen hier
			automatisch, sobald ein <a href="/agents">Agent</a> läuft. Erkannte Container ohne Label lassen sich
			unter <a href="/containers">Container</a> freigeben.
		</p>
	{:else}
		<div class="table-wrap">
			<table>
				<thead>
					<tr><th>Hostname</th><th>Quelle</th><th>Ziel</th><th>Status</th><th></th></tr>
				</thead>
				<tbody>
					{#each routes as route (route.id)}
						{@const info = statusInfo[route.status]}
						<tr>
							<td>
								{#if route.status === 'active'}
									<a href="https://{route.hostname}" target="_blank" rel="noreferrer">{route.hostname}</a>
								{:else}
									{route.hostname}
								{/if}
							</td>
							<td>
								<span class="badge">{sourceLabel[route.source]}</span>
								{#if route.agent_name}<span class="muted small">{route.agent_name}</span>{/if}
							</td>
							<td><code>{targetText(route)}</code></td>
							<td>
								<span class="badge {info.kind}">{info.label}</span>
								{#if route.detail || info.help}
									<div class="muted small">{route.detail || info.help}</div>
								{/if}
							</td>
							<td class="actions">
								{#if route.source === 'manual'}
									<button onclick={() => edit(route)}>Bearbeiten</button>
								{/if}
								{#if route.source !== 'label'}
									<button class="danger" onclick={() => remove(route)}>Löschen</button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

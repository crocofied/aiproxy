<script lang="ts">
	import { onMount } from 'svelte';
	import { api, errorText, poll } from '#lib/api.ts';
	import type { Workload } from '#lib/types.ts';

	let workloads = $state<Workload[] | null>(null);
	let error = $state('');

	// The workload currently being approved, with its form values.
	let openKey = $state('');
	let form = $state({ hostname: '', container_port: 0, backend_https: false });
	let formError = $state('');

	const key = (w: Workload) => `${w.agent_id}/${w.workload}`;
	// Label-controlled containers already have a route (or a visible
	// problem) on the routes page; they are not listed as "pending".
	const pending = $derived((workloads ?? []).filter((w) => !w.routed && !w.label_enabled));
	const routed = $derived((workloads ?? []).filter((w) => w.routed || w.label_enabled));

	async function load() {
		try {
			workloads = await api<Workload[]>('GET', '/api/containers');
			error = '';
		} catch (e) {
			error = errorText(e);
		}
	}
	onMount(() => poll(load, 5000));

	function open(w: Workload) {
		openKey = key(w);
		formError = '';
		const firstReachable = w.ports.find((p) => p.reachable);
		form = {
			hostname: w.suggested_hostname,
			container_port: firstReachable?.container_port ?? 0,
			backend_https: false
		};
	}

	async function approve(event: SubmitEvent, w: Workload) {
		event.preventDefault();
		formError = '';
		try {
			await api('POST', '/api/routes', {
				source: 'ui',
				agent_id: w.agent_id,
				workload: w.workload,
				...form
			});
			openKey = '';
			await load();
		} catch (e) {
			formError = errorText(e);
		}
	}
</script>

<h1>Container</h1>
{#if error}<div class="notice bad">{error}</div>{/if}

<div class="card">
	<h2>Erkannt, nicht freigegeben</h2>
	{#if workloads === null}
		<p class="muted">Lädt …</p>
	{:else if pending.length === 0}
		<p class="empty">
			Keine. Hier erscheinen laufende Container mit veröffentlichten Ports, die kein Label
			<code>proxy.enable=true</code> tragen.
		</p>
	{:else}
		<div class="table-wrap">
			<table>
				<thead>
					<tr><th>Container</th><th>Agent</th><th>Veröffentlichte Ports</th><th></th></tr>
				</thead>
				<tbody>
					{#each pending as w (key(w))}
						<tr>
							<td>
								<strong>{w.workload}</strong>
								<div class="muted small">{w.image}</div>
							</td>
							<td>
								{w.agent_name}
								{#if !w.agent_online}<span class="badge warn">offline</span>{/if}
							</td>
							<td>
								{#each w.ports as p (p.host_port)}
									<div>
										<code>{p.host_port} → {p.container_port}</code>
										{#if !p.reachable}<span class="badge bad">nur 127.0.0.1</span>{/if}
									</div>
								{:else}
									<span class="muted">kein TCP-Port</span>
								{/each}
							</td>
							<td class="actions">
								<button onclick={() => open(w)} disabled={!w.ports.some((p) => p.reachable)}>Freigeben</button>
							</td>
						</tr>
						{#if openKey === key(w)}
							<tr>
								<td colspan="4">
									<form onsubmit={(e) => approve(e, w)}>
										{#if formError}<div class="notice bad">{formError}</div>{/if}
										<div class="row">
											<label>
												Hostname
												<input type="text" bind:value={form.hostname} required autocomplete="off" />
											</label>
											<label>
												Container-Port
												<select bind:value={form.container_port}>
													{#each w.ports.filter((p) => p.reachable) as p (p.host_port)}
														<option value={p.container_port}>{p.container_port} (Host-Port {p.host_port})</option>
													{/each}
												</select>
											</label>
										</div>
										<label class="check">
											<input type="checkbox" bind:checked={form.backend_https} />
											<span>Container nutzt selbstsigniertes HTTPS</span>
										</label>
										<div class="row">
											<button class="primary fixed">Route anlegen</button>
											<button type="button" class="fixed" onclick={() => (openKey = '')}>Abbrechen</button>
										</div>
									</form>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

{#if routed.length > 0}
	<div class="card">
		<h2>Bereits geroutet</h2>
		<div class="table-wrap">
			<table>
				<thead>
					<tr><th>Container</th><th>Agent</th><th>Quelle</th></tr>
				</thead>
				<tbody>
					{#each routed as w (key(w))}
						<tr>
							<td>
								<strong>{w.workload}</strong>
								{#if w.containers.length > 1}<span class="muted small">({w.containers.length} Instanzen)</span>{/if}
								<div class="muted small">{w.image}</div>
							</td>
							<td>{w.agent_name}</td>
							<td><span class="badge">{w.label_enabled ? 'Label' : 'UI'}</span></td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<p class="muted small">Status und Ziel stehen unter <a href="/">Routen</a>.</p>
	</div>
{/if}

<script lang="ts">
	// Form for the DNS provider and its credentials. Cloudflare gets its
	// own simple form; every other provider supported by Traefik is
	// configured with its provider code and environment variables.
	let {
		provider = $bindable(),
		env = $bindable()
	}: { provider: string; env: Record<string, string> } = $props();

	let other = $state(provider !== '' && provider !== 'cloudflare');
	let token = $state('');
	let rows = $state<{ key: string; value: string }[]>([{ key: '', value: '' }]);

	// Keep the bound values in sync with what is typed.
	$effect(() => {
		if (other) {
			const next: Record<string, string> = {};
			for (const row of rows) {
				if (row.key.trim() !== '' && row.value !== '') next[row.key.trim().toUpperCase()] = row.value;
			}
			env = next;
		} else {
			provider = 'cloudflare';
			env = token === '' ? {} : { CF_DNS_API_TOKEN: token };
		}
	});
</script>

<label>
	DNS-Anbieter
	<select
		value={other ? 'other' : 'cloudflare'}
		onchange={(e) => {
			other = e.currentTarget.value === 'other';
			if (other && provider === 'cloudflare') provider = '';
		}}
	>
		<option value="cloudflare">Cloudflare</option>
		<option value="other">Anderer Anbieter (laut Traefik-Doku)</option>
	</select>
</label>

{#if other}
	<label>
		Provider-Code
		<input type="text" bind:value={provider} placeholder="z. B. hetzner, ionos, desec, route53" autocomplete="off" />
		<span class="hint">
			Der „Provider Code“ aus der Tabelle der
			<a href="https://doc.traefik.io/traefik/reference/install-configuration/tls/certificate-resolvers/acme/#providers" target="_blank" rel="noreferrer">Traefik-Dokumentation</a>.
		</span>
	</label>
	<p class="small"><strong>Umgebungsvariablen des Anbieters</strong> (Spalte „Environment Variables“ derselben Tabelle)</p>
	{#each rows as row, i (i)}
		<div class="row">
			<input type="text" bind:value={row.key} placeholder="NAME, z. B. HETZNER_API_TOKEN" autocomplete="off" aria-label="Name der Variable" />
			<input type="password" bind:value={row.value} placeholder="Wert" autocomplete="off" aria-label="Wert der Variable" />
			<button type="button" class="fixed" onclick={() => rows.splice(i, 1)} disabled={rows.length === 1}>Entfernen</button>
		</div>
	{/each}
	<p><button type="button" onclick={() => rows.push({ key: '', value: '' })}>Weitere Variable</button></p>
{:else}
	<label>
		Cloudflare-API-Token
		<input type="password" bind:value={token} autocomplete="off" />
		<span class="hint">
			Ein Token mit der Berechtigung „Zone → DNS → Bearbeiten“, beschränkt auf die eine Zone. Die
			Anleitung steht im README und auf der Hilfeseite.
		</span>
	</label>
{/if}

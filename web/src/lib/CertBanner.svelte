<script lang="ts">
	// Shown on every page, so a failing renewal cannot be overlooked.
	import { cert } from './state.svelte';
	import { formatDate } from './api';

	const c = $derived(cert.current);
</script>

{#if c}
	{#if c.state === 'expired'}
		<div class="notice bad">
			<strong>Das Zertifikat ist abgelaufen</strong> (seit {formatDate(c.expires_at)}). Browser zeigen
			Warnungen an. Details unter <a href="/settings">Einstellungen</a>.
		</div>
	{:else if c.state === 'expiring'}
		<div class="notice warn">
			<strong>Das Zertifikat läuft in {c.days_left} Tagen ab</strong> und wurde nicht erneuert. Details
			unter <a href="/settings">Einstellungen</a>.
		</div>
	{/if}
	{#if c.last_error}
		<div class="notice bad">
			<strong>
				{c.state === 'none' ? 'Das Zertifikat konnte nicht ausgestellt werden' : 'Die Erneuerung des Zertifikats ist fehlgeschlagen'}
			</strong>
			({formatDate(c.last_error_at)}): {c.last_error}
		</div>
	{:else if c.state === 'none'}
		<div class="notice">
			Das Zertifikat für <code>*.{c.base_domain}</code> wird angefordert. Das dauert per DNS-Challenge
			meist ein bis drei Minuten.
		</div>
	{/if}
	{#if c.staging}
		<div class="notice warn">
			<strong>Staging-Modus:</strong> Let's Encrypt stellt nur Test-Zertifikate aus, denen Browser nicht
			vertrauen. Umschalten unter <a href="/settings">Einstellungen</a>.
		</div>
	{/if}
{/if}

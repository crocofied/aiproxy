<script lang="ts">
	import { onMount } from 'svelte';
	import { api, errorText, formatDate } from '#lib/api.ts';
	import { cert, refreshCert } from '#lib/state.svelte.ts';
	import DnsCredentials from '#lib/DnsCredentials.svelte';
	import DnsHints from '#lib/DnsHints.svelte';
	import type { SettingsResponse } from '#lib/types.ts';

	let settings = $state<SettingsResponse | null>(null);
	let resolvers = $state('');
	let error = $state('');
	let saved = $state(false);
	let busy = $state(false);

	// Credentials are only sent when the admin chooses to replace them.
	let replaceCredentials = $state(false);
	let dnsEnv = $state<Record<string, string>>({});

	let passwords = $state({ current: '', new: '', repeat: '' });
	let passwordMessage = $state<{ ok: boolean; text: string } | null>(null);

	const c = $derived(cert.current);

	function show(s: SettingsResponse) {
		settings = s;
		resolvers = s.dns_resolvers.join(', ');
	}

	onMount(async () => {
		try {
			show(await api<SettingsResponse>('GET', '/api/settings'));
			refreshCert();
		} catch (e) {
			error = errorText(e);
		}
	});

	async function save(event: SubmitEvent) {
		event.preventDefault();
		if (!settings) return;
		busy = true;
		error = '';
		saved = false;
		try {
			// Send only the fields the API knows.
			const { ui_host: _host, dns_env_keys: _keys, ...values } = settings;
			const body: Record<string, unknown> = {
				...values,
				dns_resolvers: resolvers.split(',').map((r) => r.trim()).filter((r) => r !== '')
			};
			if (replaceCredentials) body.dns_env = dnsEnv;
			show(await api<SettingsResponse>('PUT', '/api/settings', body));
			replaceCredentials = false;
			saved = true;
			refreshCert();
		} catch (e) {
			error = errorText(e);
		} finally {
			busy = false;
		}
	}

	async function changePassword(event: SubmitEvent) {
		event.preventDefault();
		passwordMessage = null;
		if (passwords.new !== passwords.repeat) {
			passwordMessage = { ok: false, text: 'Die beiden neuen Passwörter stimmen nicht überein' };
			return;
		}
		try {
			await api('POST', '/api/password', { current: passwords.current, new: passwords.new });
			passwords = { current: '', new: '', repeat: '' };
			passwordMessage = { ok: true, text: 'Das Passwort wurde geändert. Andere Sitzungen wurden abgemeldet.' };
		} catch (e) {
			passwordMessage = { ok: false, text: errorText(e) };
		}
	}
</script>

<h1>Einstellungen</h1>

<div class="card">
	<h2>Zertifikat</h2>
	{#if !c}
		<p class="muted">Lädt …</p>
	{:else}
		<div class="table-wrap">
			<table class="facts">
				<tbody>
					<tr>
						<th>Status</th>
						<td>
							{#if c.state === 'valid'}<span class="badge ok">Gültig</span>
							{:else if c.state === 'expiring'}<span class="badge warn">Läuft bald ab</span>
							{:else if c.state === 'expired'}<span class="badge bad">Abgelaufen</span>
							{:else}<span class="badge">Noch nicht ausgestellt</span>{/if}
							{#if c.staging}<span class="badge warn">Staging</span>{/if}
						</td>
					</tr>
					<tr><th>Gilt für</th><td>{c.domains?.join(', ') ?? `${c.base_domain}, *.${c.base_domain}`}</td></tr>
					{#if c.state !== 'none'}
						<tr><th>Läuft ab</th><td>{formatDate(c.expires_at)} (noch {c.days_left} Tage)</td></tr>
						<tr><th>Letzte Erneuerung</th><td>{formatDate(c.issued_at)}</td></tr>
						<tr><th>Aussteller</th><td>{c.issuer}</td></tr>
					{/if}
					{#if c.last_error}
						<tr><th>Letzter Fehler</th><td>{formatDate(c.last_error_at)}: {c.last_error}</td></tr>
					{/if}
				</tbody>
			</table>
		</div>
		<p class="muted small">
			Traefik erneuert das Zertifikat selbständig, lange bevor es abläuft. Warnungen erscheinen, wenn eine
			Erneuerung fehlschlägt oder weniger als 14 Tage verbleiben.
		</p>
	{/if}
</div>

{#if error}<div class="notice bad">{error}</div>{/if}
{#if saved}<div class="notice ok">Gespeichert. Falls nötig, wurde Traefik neu gestartet.</div>{/if}

{#if settings}
	<form onsubmit={save}>
		<div class="card">
			<h2>Domain und Zertifikat</h2>
			<div class="row">
				<label>
					Basis-Domain
					<input type="text" bind:value={settings.base_domain} required autocomplete="off" />
				</label>
				<label>
					Subdomain für die Oberfläche
					<input type="text" bind:value={settings.ui_subdomain} required autocomplete="off" />
				</label>
				<label>
					E-Mail-Adresse für Let's Encrypt
					<input type="email" bind:value={settings.acme_email} required autocomplete="off" />
				</label>
			</div>
			<p class="muted small">
				Nach einer Änderung der Basis-Domain müssen manuelle und per UI freigegebene Routen auf die neue
				Domain umgestellt werden. Label-Routen passen sich von selbst an.
			</p>
			<label class="check">
				<input type="checkbox" bind:checked={settings.staging} />
				<span>
					Staging-Umgebung von Let's Encrypt verwenden
					<span class="hint">Test-Zertifikate ohne Vertrauen im Browser. Zum Ausprobieren der Einrichtung.</span>
				</span>
			</label>

			<h3>DNS-Anbieter</h3>
			<p>
				Anbieter: <code>{settings.dns_provider}</code>. Gespeicherte Zugangsdaten:
				{#if settings.dns_env_keys.length > 0}
					{#each settings.dns_env_keys as key (key)}<code>{key}</code>{' '}{/each}
					<span class="muted small">(Werte werden nie angezeigt)</span>
				{:else}
					<span class="badge bad">keine</span>
				{/if}
			</p>
			<label class="check">
				<input type="checkbox" bind:checked={replaceCredentials} />
				<span>Anbieter oder Zugangsdaten ersetzen</span>
			</label>
			{#if replaceCredentials}
				<DnsCredentials bind:provider={settings.dns_provider} bind:env={dnsEnv} />
			{/if}

			<h3>Prüfung des DNS-Eintrags</h3>
			<label>
				Öffentliche DNS-Resolver
				<input type="text" bind:value={resolvers} autocomplete="off" />
				<span class="hint">
					Kommagetrennt. Über diese Server prüft Traefik, ob der Challenge-Eintrag sichtbar ist. Lokale
					DNS-Server eignen sich nicht, wenn sie die Domain auf den Proxy umschreiben.
				</span>
			</label>
			<label class="check">
				<input type="checkbox" bind:checked={settings.skip_propagation_check} />
				<span>
					Prüfung überspringen und stattdessen warten
					<span class="hint">Nur nötig, wenn das Netz ausgehende DNS-Anfragen blockiert oder umleitet.</span>
				</span>
			</label>
			{#if settings.skip_propagation_check}
				<label>
					Wartezeit in Sekunden
					<input type="number" bind:value={settings.propagation_delay_seconds} min="0" max="3600" />
				</label>
			{/if}
		</div>

		<div class="card">
			<h2>Routen</h2>
			<div class="row">
				<label>
					Hostname-Muster für Label-Routen
					<input type="text" bind:value={settings.hostname_pattern} required autocomplete="off" />
					<span class="hint">
						Platzhalter: <code>{'{service}'}</code>, <code>{'{project}'}</code>,
						<code>{'{container}'}</code>, <code>{'{agent}'}</code>, <code>{'{domain}'}</code>.
					</span>
				</label>
				<label>
					Agent-Timeout in Sekunden
					<input type="number" bind:value={settings.agent_timeout_seconds} min="30" max="86400" required />
					<span class="hint">Meldet sich ein Agent so lange nicht, werden seine Routen abgeschaltet.</span>
				</label>
			</div>
		</div>

		<p><button class="primary" disabled={busy}>{busy ? 'Speichert …' : 'Einstellungen speichern'}</button></p>
	</form>

	<form class="card" onsubmit={changePassword}>
		<h2>Admin-Passwort ändern</h2>
		{#if passwordMessage}
			<div class="notice {passwordMessage.ok ? 'ok' : 'bad'}">{passwordMessage.text}</div>
		{/if}
		<div class="row">
			<label>
				Aktuelles Passwort
				<input type="password" bind:value={passwords.current} autocomplete="current-password" required />
			</label>
			<label>
				Neues Passwort
				<input type="password" bind:value={passwords.new} autocomplete="new-password" minlength="8" required />
			</label>
			<label>
				Neues Passwort wiederholen
				<input type="password" bind:value={passwords.repeat} autocomplete="new-password" required />
			</label>
		</div>
		<button>Passwort ändern</button>
	</form>

	<div class="card">
		<h2>Lokaler DNS-Eintrag</h2>
		<DnsHints baseDomain={settings.base_domain} />
	</div>
{/if}

<style>
	.facts {
		min-width: 0;
	}
	.facts th {
		width: 11rem;
	}
</style>

<script lang="ts">
	import { onMount } from 'svelte';
	import { api, errorText } from '#lib/api.ts';
	import { refreshSession } from '#lib/state.svelte.ts';
	import DnsCredentials from '#lib/DnsCredentials.svelte';
	import DnsHints from '#lib/DnsHints.svelte';
	import type { Settings, SetupDefaults } from '#lib/types.ts';

	let step = $state(1);
	let error = $state('');
	let busy = $state(false);

	let settings = $state<Settings | null>(null);
	let passwordPreset = $state(false);
	let dnsEnvPreset = $state<string[]>([]);

	let password = $state('');
	let passwordRepeat = $state('');
	let dnsEnv = $state<Record<string, string>>({});
	let uiHost = $state('');

	onMount(async () => {
		try {
			const defaults = await api<SetupDefaults>('GET', '/api/setup');
			settings = defaults.settings;
			passwordPreset = defaults.password_preset;
			dnsEnvPreset = defaults.dns_env_preset;
		} catch (e) {
			error = errorText(e);
		}
	});

	// Checks the fields of the current step before moving on. The server
	// validates everything again when the wizard is submitted.
	function next() {
		error = '';
		if (!settings) return;
		if (step === 1) {
			if (password === '' && !passwordPreset) error = 'Bitte ein Passwort vergeben';
			else if (password !== '' && password.length < 8) error = 'Das Passwort muss mindestens 8 Zeichen lang sein';
			else if (password !== passwordRepeat) error = 'Die beiden Passwörter stimmen nicht überein';
		} else if (step === 2) {
			if (!settings.base_domain.includes('.')) error = 'Bitte die Basis-Domain angeben, z. B. home.example.com';
			else if (settings.ui_subdomain.trim() === '') error = 'Bitte eine Subdomain für die Oberfläche angeben';
			else if (!settings.acme_email.includes('@')) error = 'Bitte eine E-Mail-Adresse angeben';
		}
		if (error === '') step += 1;
	}

	async function finish() {
		if (!settings) return;
		busy = true;
		error = '';
		try {
			const result = await api<{ ui_host: string }>('POST', '/api/setup', {
				...settings,
				password,
				dns_env: dnsEnv
			});
			uiHost = result.ui_host;
			step = 4;
		} catch (e) {
			error = errorText(e);
		} finally {
			busy = false;
		}
	}
</script>

<h1>lanproxy einrichten</h1>

{#if !settings}
	{#if error}<div class="notice bad">{error}</div>{:else}<p class="muted">Lädt …</p>{/if}
{:else}
	{#if step < 4}<p class="muted">Schritt {step} von 3</p>{/if}
	{#if error}<div class="notice bad">{error}</div>{/if}

	{#if step === 1}
		<div class="card">
			<h2>Admin-Passwort</h2>
			{#if passwordPreset}
				<div class="notice">
					Ein Passwort ist per Umgebungsvariable vorgegeben. Die Felder können leer bleiben, um es zu
					übernehmen.
				</div>
			{/if}
			<label>
				Passwort
				<input type="password" bind:value={password} autocomplete="new-password" />
				<span class="hint">Mindestens 8 Zeichen. Es gibt genau einen Benutzer.</span>
			</label>
			<label>
				Passwort wiederholen
				<input type="password" bind:value={passwordRepeat} autocomplete="new-password" />
			</label>
			<button class="primary" onclick={next}>Weiter</button>
		</div>
	{:else if step === 2}
		<div class="card">
			<h2>Domain</h2>
			<label>
				Basis-Domain
				<input type="text" bind:value={settings.base_domain} placeholder="home.example.com" autocomplete="off" />
				<span class="hint">
					Dienste werden unter Subdomains davon erreichbar, z. B. <code>wiki.{settings.base_domain || 'home.example.com'}</code>.
					Das Zertifikat gilt für die Domain und alle direkten Subdomains.
				</span>
			</label>
			<label>
				Subdomain für diese Oberfläche
				<input type="text" bind:value={settings.ui_subdomain} placeholder="proxy" autocomplete="off" />
				<span class="hint">
					Ergibt <code>{settings.ui_subdomain || 'proxy'}.{settings.base_domain || 'home.example.com'}</code>.
				</span>
			</label>
			<label>
				E-Mail-Adresse für Let's Encrypt
				<input type="email" bind:value={settings.acme_email} autocomplete="off" />
				<span class="hint">Wird für das Konto bei Let's Encrypt verwendet.</span>
			</label>
			<div class="row">
				<button class="fixed" onclick={() => (step = 1)}>Zurück</button>
				<button class="primary fixed" onclick={next}>Weiter</button>
			</div>
		</div>
	{:else if step === 3}
		<div class="card">
			<h2>Zertifikat</h2>
			<p>
				Das Zertifikat wird per DNS-Challenge ausgestellt: lanproxy legt über die API des DNS-Anbieters
				kurzzeitig einen TXT-Eintrag an. Der Server muss dafür nicht aus dem Internet erreichbar sein.
			</p>
			{#if dnsEnvPreset.length > 0}
				<div class="notice">
					Per Umgebungsvariable vorgegeben: {dnsEnvPreset.join(', ')}. Die Felder für Zugangsdaten können
					leer bleiben.
				</div>
			{/if}
			<DnsCredentials bind:provider={settings.dns_provider} bind:env={dnsEnv} />
			<label class="check">
				<input type="checkbox" bind:checked={settings.staging} />
				<span>
					Staging-Umgebung von Let's Encrypt verwenden
					<span class="hint">
						Zum Testen: stellt Zertifikate aus, denen Browser nicht vertrauen, hat aber großzügige
						Limits. Lässt sich später in den Einstellungen umschalten.
					</span>
				</span>
			</label>
			<div class="row">
				<button class="fixed" onclick={() => (step = 2)} disabled={busy}>Zurück</button>
				<button class="primary fixed" onclick={finish} disabled={busy}>
					{busy ? 'Wird eingerichtet …' : 'Einrichtung abschließen'}
				</button>
			</div>
		</div>
	{:else}
		<div class="notice ok">
			<strong>Die Einrichtung ist abgeschlossen.</strong> Das Zertifikat wird jetzt im Hintergrund
			angefordert.
		</div>
		<div class="card">
			<h2>Letzter Schritt: lokaler DNS-Eintrag</h2>
			<DnsHints baseDomain={settings.base_domain} />
			<p>
				Danach ist die Oberfläche unter <a href="https://{uiHost}">https://{uiHost}</a> erreichbar. Der
				Zugang über IP-Adresse und Port bleibt zusätzlich bestehen.
			</p>
			<!-- Only now is the session refreshed, so the layout does not
			     leave the wizard before this page has been read. -->
			<button class="primary" onclick={refreshSession}>Zur Übersicht</button>
		</div>
	{/if}
{/if}

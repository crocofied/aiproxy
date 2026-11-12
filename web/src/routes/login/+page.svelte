<script lang="ts">
	import { api, errorText } from '#lib/api.ts';
	import { session } from '#lib/state.svelte.ts';

	let password = $state('');
	let error = $state('');
	let busy = $state(false);

	async function login(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		error = '';
		try {
			await api('POST', '/api/login', { password });
			session.authenticated = true; // the layout now shows the application
		} catch (e) {
			error = errorText(e);
		} finally {
			busy = false;
		}
	}
</script>

<h1>lanproxy</h1>
<form class="card" onsubmit={login}>
	<h2>Anmelden</h2>
	{#if error}<div class="notice bad">{error}</div>{/if}
	<label>
		Admin-Passwort
		<!-- svelte-ignore a11y_autofocus -->
		<input type="password" bind:value={password} autocomplete="current-password" autofocus required />
	</label>
	<button class="primary" disabled={busy}>Anmelden</button>
</form>

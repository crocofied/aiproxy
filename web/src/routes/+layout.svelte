<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, errorText } from '#lib/api.ts';
	import { session, refreshSession, refreshCert } from '#lib/state.svelte.ts';
	import CertBanner from '#lib/CertBanner.svelte';

	let { children } = $props();
	let loadError = $state('');

	const path = $derived(page.url.pathname);
	// The wizard and the login page are shown without navigation.
	const bare = $derived(path === '/setup' || path === '/login');

	const nav = [
		{ href: '/', label: 'Routen' },
		{ href: '/containers', label: 'Container' },
		{ href: '/agents', label: 'Agents' },
		{ href: '/settings', label: 'Einstellungen' },
		{ href: '/help', label: 'Hilfe' }
	];

	onMount(() => {
		refreshSession().catch((e) => (loadError = errorText(e)));
		const timer = setInterval(refreshCert, 60_000);
		return () => clearInterval(timer);
	});

	// Send the visitor where they belong: wizard, login or application.
	$effect(() => {
		if (!session.loaded) return;
		if (!session.setupDone) {
			if (path !== '/setup') goto('/setup');
		} else if (!session.authenticated) {
			if (path !== '/login') goto('/login');
		} else {
			if (bare) goto('/');
			refreshCert();
		}
	});

	async function logout() {
		await api('POST', '/api/logout').catch(() => {});
		session.authenticated = false;
	}
</script>

{#if loadError}
	<main><div class="notice bad">{loadError}</div></main>
{:else if !session.loaded}
	<main><p class="muted">Lädt …</p></main>
{:else if bare}
	<main class="narrow">{@render children()}</main>
{:else if session.authenticated}
	<header>
		<div class="inner">
			<strong class="brand">lanproxy</strong>
			<nav>
				{#each nav as item (item.href)}
					<a href={item.href} class:current={path === item.href}>{item.label}</a>
				{/each}
			</nav>
			<button class="link" onclick={logout}>Abmelden</button>
		</div>
	</header>
	<main>
		<CertBanner />
		{@render children()}
		<p class="muted small version">lanproxy {session.version}</p>
	</main>
{/if}

<style>
	header {
		background: var(--surface);
		border-bottom: 1px solid var(--border);
	}
	.inner {
		max-width: 68rem;
		margin: 0 auto;
		padding: 0.6rem 1rem;
		display: flex;
		align-items: center;
		gap: 1.25rem;
		flex-wrap: wrap;
	}
	.brand {
		font-size: 1.1rem;
	}
	nav {
		display: flex;
		gap: 0.25rem;
		flex: 1;
		flex-wrap: wrap;
	}
	nav a {
		padding: 0.3rem 0.7rem;
		border-radius: 6px;
		color: var(--text);
		text-decoration: none;
	}
	nav a.current {
		background: var(--info-bg);
		font-weight: 600;
	}
	main {
		max-width: 68rem;
		margin: 0 auto;
		padding: 1.25rem 1rem 2rem;
	}
	main.narrow {
		max-width: 40rem;
	}
	.version {
		margin-top: 2rem;
		text-align: center;
	}
</style>

// State shared by all pages. "$state" makes the objects reactive: a
// component that reads a field is updated when the field changes.
import { api } from './api';
import type { Certificate } from './types';

export const session = $state({
	loaded: false,
	setupDone: false,
	authenticated: false,
	version: ''
});

export const cert = $state<{ current: Certificate | null }>({ current: null });

export async function refreshSession(): Promise<void> {
	const status = await api<{ setup_done: boolean; authenticated: boolean; version: string }>(
		'GET',
		'/api/status'
	);
	session.setupDone = status.setup_done;
	session.authenticated = status.authenticated;
	session.version = status.version;
	session.loaded = true;
}

export async function refreshCert(): Promise<void> {
	if (!session.authenticated) return;
	try {
		cert.current = await api<Certificate>('GET', '/api/certificate');
	} catch {
		// The banner is a convenience; pages show their own errors.
	}
}

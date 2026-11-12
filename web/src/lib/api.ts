import { session } from './state.svelte';

/** An error answer of the API; "message" is meant for the admin. */
export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

/** Calls the controller's JSON API. The session cookie is sent automatically. */
export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
	let response: Response;
	try {
		response = await fetch(path, {
			method,
			headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body)
		});
	} catch {
		throw new ApiError(0, 'Der Controller ist nicht erreichbar');
	}

	if (response.status === 204) return undefined as T;

	let data: unknown = null;
	try {
		data = await response.json();
	} catch {
		// not JSON, handled below
	}
	if (!response.ok) {
		// Session expired: the layout sends the user to the login page.
		if (response.status === 401 && path !== '/api/login') session.authenticated = false;
		const message = (data as { error?: string } | null)?.error ?? `Fehler ${response.status}`;
		throw new ApiError(response.status, message);
	}
	return data as T;
}

/** Turns any thrown value into a message for display. */
export function errorText(e: unknown): string {
	return e instanceof Error ? e.message : String(e);
}

/** Calls fn now and then every "ms" milliseconds; returns a stop function. */
export function poll(fn: () => void, ms: number): () => void {
	fn();
	const timer = setInterval(fn, ms);
	return () => clearInterval(timer);
}

/** "vor 12 s", "vor 5 min", ... */
export function ago(iso: string | null | undefined): string {
	if (!iso) return 'nie';
	const seconds = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
	if (seconds < 90) return `vor ${seconds} s`;
	if (seconds < 5400) return `vor ${Math.round(seconds / 60)} min`;
	if (seconds < 129600) return `vor ${Math.round(seconds / 3600)} h`;
	return `vor ${Math.round(seconds / 86400)} Tagen`;
}

export function formatDate(iso: string | undefined): string {
	return iso ? new Date(iso).toLocaleString('de-DE', { dateStyle: 'medium', timeStyle: 'short' }) : '–';
}

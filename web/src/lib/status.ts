import type { Route, RouteStatus } from './types';

/** German text and colour for every route status. */
export const statusInfo: Record<RouteStatus, { label: string; kind: 'ok' | 'warn' | 'bad'; help: string }> = {
	active: { label: 'Aktiv', kind: 'ok', help: '' },
	conflict: {
		label: 'Konflikt',
		kind: 'bad',
		help: 'Der Hostname gehört bereits einer anderen Route. Die bestehende Route bleibt unverändert.'
	},
	invalid: { label: 'Ungültig', kind: 'bad', help: '' },
	agent_offline: {
		label: 'Agent offline',
		kind: 'warn',
		help: 'Der Agent hat sich zu lange nicht gemeldet, die Route ist abgeschaltet.'
	},
	container_missing: { label: 'Container läuft nicht', kind: 'warn', help: '' },
	port_ambiguous: {
		label: 'Port unklar',
		kind: 'bad',
		help: 'Der Container veröffentlicht mehrere Ports. Mit dem Label proxy.port den Container-Port wählen.'
	},
	port_unpublished: {
		label: 'Port nicht veröffentlicht',
		kind: 'bad',
		help: 'Der gewählte Container-Port ist nicht auf dem Host veröffentlicht (ports: in der Compose-Datei).'
	},
	port_local_only: {
		label: 'Nur lokal gebunden',
		kind: 'bad',
		help: 'Der Port ist nur an 127.0.0.1 gebunden und vom Proxy aus nicht erreichbar.'
	}
};

export const sourceLabel: Record<Route['source'], string> = {
	label: 'Label',
	ui: 'UI',
	manual: 'Manuell'
};

/** What a route points at, for display. */
export function targetText(route: Route): string {
	if (route.source === 'manual') {
		return `${route.backend_https ? 'https' : 'http'}://${route.target_host}:${route.target_port}`;
	}
	if (route.servers.length > 0) return route.servers.join(', ');
	const port = route.container_port ? ` (Port ${route.container_port})` : '';
	return `${route.workload}${port}`;
}

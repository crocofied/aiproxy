// These types mirror the JSON of the controller's API
// (internal/controller/httpapi).

export interface Settings {
	base_domain: string;
	ui_subdomain: string;
	acme_email: string;
	dns_provider: string;
	staging: boolean;
	dns_resolvers: string[];
	skip_propagation_check: boolean;
	propagation_delay_seconds: number;
	hostname_pattern: string;
	agent_timeout_seconds: number;
}

export interface SettingsResponse extends Settings {
	ui_host: string;
	/** Names of the stored DNS credentials. The values never leave the server. */
	dns_env_keys: string[];
}

export interface SetupDefaults {
	settings: Settings;
	password_preset: boolean;
	dns_env_preset: string[];
}

export type RouteStatus =
	| 'active'
	| 'conflict'
	| 'invalid'
	| 'agent_offline'
	| 'container_missing'
	| 'port_ambiguous'
	| 'port_unpublished'
	| 'port_local_only';

export interface Route {
	id: number;
	source: 'label' | 'ui' | 'manual';
	hostname: string;
	agent_id?: number;
	agent_name?: string;
	workload?: string;
	container_port?: number;
	target_host?: string;
	target_port?: number;
	backend_https: boolean;
	status: RouteStatus;
	detail?: string;
	servers: string[];
}

export interface Workload {
	agent_id: number;
	agent_name: string;
	agent_online: boolean;
	workload: string;
	containers: string[];
	image: string;
	label_enabled: boolean;
	ports: { container_port: number; host_port: number; reachable: boolean }[];
	routed: boolean;
	suggested_hostname: string;
}

export interface Agent {
	id: number;
	name: string;
	hostname: string;
	host_ip: string;
	observed_ip: string;
	version: string;
	online: boolean;
	last_seen_at: string | null;
	created_at: string;
	containers: number;
}

export interface AgentCreated {
	agent: Agent;
	token: string;
	image: string;
	build_context: string;
}

export interface Certificate {
	state: 'none' | 'valid' | 'expiring' | 'expired';
	domains?: string[];
	issuer?: string;
	issued_at?: string;
	expires_at?: string;
	days_left: number;
	base_domain: string;
	staging: boolean;
	last_error?: string;
	last_error_at?: string;
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"lanproxy/api"
)

// Agent is a registered Docker host. The token hash is intentionally not
// a field: it is only ever needed for authentication (AgentTokenHash)
// and so cannot leak into an API response by accident.
type Agent struct {
	ID       int64
	Name     string
	Hostname string
	// HostIP is the address the agent reported for itself.
	HostIP string
	// ObservedIP is the address its last report came from.
	ObservedIP string
	Version    string
	CreatedAt  time.Time
	// LastSeenAt is the zero time if the agent never reported.
	LastSeenAt time.Time
}

// Online reports whether the agent has been heard from within timeout.
func (a Agent) Online(now time.Time, timeout time.Duration) bool {
	return !a.LastSeenAt.IsZero() && now.Sub(a.LastSeenAt) <= timeout
}

// Container is a container as reported by an agent.
type Container struct {
	AgentID int64
	// Workload identifies what the container is across restarts and
	// image updates, see routing.Workload.
	Workload string
	api.Container
}

// AgentReport is the non-container part of a report.
type AgentReport struct {
	Hostname   string
	HostIP     string
	ObservedIP string
	Version    string
}

func timeFromUnix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func scanAgent(rows *sql.Rows) (Agent, error) {
	var a Agent
	var created, lastSeen int64
	err := rows.Scan(&a.ID, &a.Name, &a.Hostname, &a.HostIP, &a.ObservedIP, &a.Version, &created, &lastSeen)
	a.CreatedAt, a.LastSeenAt = timeFromUnix(created), timeFromUnix(lastSeen)
	return a, err
}

// Agents returns all agents ordered by name.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	return collect(ctx, s.db, scanAgent,
		`SELECT id, name, hostname, host_ip, observed_ip, version, created_at, last_seen_at
		 FROM agents ORDER BY name`)
}

// CreateAgent registers a new agent. tokenHash is the hash of its token;
// the token itself is never stored.
func (s *Store) CreateAgent(ctx context.Context, name, tokenHash string, now time.Time) (Agent, error) {
	agent := Agent{Name: name, CreatedAt: now}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM agents WHERE name = ?", name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrNameTaken
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO agents (name, token_hash, created_at) VALUES (?, ?, ?)", name, tokenHash, now.Unix())
		if err != nil {
			return err
		}
		agent.ID, err = res.LastInsertId()
		return err
	})
	return agent, err
}

// SetAgentTokenHash replaces the token of an agent.
func (s *Store) SetAgentTokenHash(ctx context.Context, id int64, tokenHash string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE agents SET token_hash = ? WHERE id = ?", tokenHash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AgentTokenHash returns the name and the stored token hash of an agent.
func (s *Store) AgentTokenHash(ctx context.Context, id int64) (name, hash string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT name, token_hash FROM agents WHERE id = ?", id).Scan(&name, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return name, hash, err
}

// DeleteAgent removes an agent together with its containers and routes
// (ON DELETE CASCADE). Hostnames it owned become available again.
func (s *Store) DeleteAgent(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM agents WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return promoteConflicts(ctx, tx)
	})
}

// ApplyReport stores a report: it marks the agent as seen, replaces its
// container snapshot and brings its label routes in line with desired.
// Everything happens in one transaction, so readers never see half of it.
func (s *Store) ApplyReport(ctx context.Context, agentID int64, report AgentReport, containers []Container, desired []LabelRoute, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE agents SET hostname = ?, host_ip = ?, observed_ip = ?, version = ?, last_seen_at = ?
			 WHERE id = ?`,
			report.Hostname, report.HostIP, report.ObservedIP, report.Version, now.Unix(), agentID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM containers WHERE agent_id = ?", agentID); err != nil {
			return err
		}
		for _, c := range containers {
			labels, err := json.Marshal(c.Labels)
			if err != nil {
				return err
			}
			ports, err := json.Marshal(c.Ports)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx,
				`INSERT INTO containers
				 (agent_id, container_id, name, image, workload, compose_project, compose_service, labels, ports)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				agentID, c.ID, c.Name, c.Image, c.Workload, c.ComposeProject, c.ComposeService,
				string(labels), string(ports))
			if err != nil {
				return err
			}
		}
		return syncLabelRoutes(ctx, tx, agentID, desired, now)
	})
}

func scanContainer(rows *sql.Rows) (Container, error) {
	var c Container
	var labels, ports string
	err := rows.Scan(&c.AgentID, &c.ID, &c.Name, &c.Image, &c.Workload,
		&c.ComposeProject, &c.ComposeService, &labels, &ports)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(labels), &c.Labels); err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(ports), &c.Ports)
	return c, err
}

// Containers returns the last known containers of all agents.
func (s *Store) Containers(ctx context.Context) ([]Container, error) {
	return collect(ctx, s.db, scanContainer,
		`SELECT agent_id, container_id, name, image, workload, compose_project, compose_service, labels, ports
		 FROM containers ORDER BY agent_id, name, container_id`)
}

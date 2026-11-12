package store

import (
	"context"
	"database/sql"
	"time"
)

// Where a route comes from.
const (
	SourceLabel  = "label"  // container labels (proxy.enable=true)
	SourceUI     = "ui"     // discovered container approved in the UI
	SourceManual = "manual" // arbitrary host:port
)

// Persistent states of a route.
const (
	StateOK       = "ok"
	StateConflict = "conflict" // another route owns the hostname
	StateInvalid  = "invalid"  // labels are unusable, see Detail
)

// Route maps a hostname to a backend.
type Route struct {
	ID       int64
	Source   string
	Hostname string
	// AgentID and Workload identify the backend of label and ui routes.
	// AgentID is 0 for manual routes.
	AgentID  int64
	Workload string
	// ContainerPort is the port inside the container; 0 means "the only
	// published port".
	ContainerPort int
	// TargetHost and TargetPort are the backend of manual routes.
	TargetHost string
	TargetPort int
	// BackendHTTPS: talk HTTPS to the backend without verifying its
	// certificate (self-signed).
	BackendHTTPS bool
	State        string
	Detail       string
	CreatedAt    time.Time
}

// LabelRoute is what the labels of one workload ask for.
type LabelRoute struct {
	Workload      string
	Hostname      string
	ContainerPort int
	BackendHTTPS  bool
	// Invalid is non-empty if the labels cannot be turned into a route;
	// it then holds the explanation shown in the UI.
	Invalid string
}

const routeColumns = `id, source, hostname, COALESCE(agent_id, 0), workload, container_port,
	target_host, target_port, backend_https, state, detail, created_at`

func scanRoute(rows *sql.Rows) (Route, error) {
	var r Route
	var created int64
	err := rows.Scan(&r.ID, &r.Source, &r.Hostname, &r.AgentID, &r.Workload, &r.ContainerPort,
		&r.TargetHost, &r.TargetPort, &r.BackendHTTPS, &r.State, &r.Detail, &created)
	r.CreatedAt = timeFromUnix(created)
	return r, err
}

// Routes returns all routes ordered by hostname.
func (s *Store) Routes(ctx context.Context) ([]Route, error) {
	return collect(ctx, s.db, scanRoute, "SELECT "+routeColumns+" FROM routes ORDER BY hostname, id")
}

// hostnameTaken reports whether an active route other than exceptID
// owns hostname.
func hostnameTaken(ctx context.Context, q querier, hostname string, exceptID int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM routes WHERE hostname = ? AND state = ? AND id != ?",
		hostname, StateOK, exceptID).Scan(&n)
	return n > 0, err
}

// nullableID stores 0 as NULL, as required by the foreign key.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func insertRoute(ctx context.Context, tx *sql.Tx, r Route) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO routes (source, hostname, agent_id, workload, container_port,
		                     target_host, target_port, backend_https, state, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Source, r.Hostname, nullableID(r.AgentID), r.Workload, r.ContainerPort,
		r.TargetHost, r.TargetPort, r.BackendHTTPS, r.State, r.Detail, r.CreatedAt.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateRoute stores a ui or manual route. If the hostname already has
// an owner it fails with ErrHostnameTaken: an existing route is never
// overwritten.
func (s *Store) CreateRoute(ctx context.Context, r Route) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		taken, err := hostnameTaken(ctx, tx, r.Hostname, 0)
		if err != nil {
			return err
		}
		if taken {
			return ErrHostnameTaken
		}
		r.State, r.Detail = StateOK, ""
		id, err = insertRoute(ctx, tx, r)
		return err
	})
	return id, err
}

// UpdateRoute changes a ui or manual route. Label routes are controlled
// by the container's labels and cannot be edited.
func (s *Store) UpdateRoute(ctx context.Context, r Route) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		taken, err := hostnameTaken(ctx, tx, r.Hostname, r.ID)
		if err != nil {
			return err
		}
		if taken {
			return ErrHostnameTaken
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE routes SET hostname = ?, container_port = ?, target_host = ?, target_port = ?, backend_https = ?
			 WHERE id = ? AND source != ?`,
			r.Hostname, r.ContainerPort, r.TargetHost, r.TargetPort, r.BackendHTTPS, r.ID, SourceLabel)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		// The old hostname may have been waited for by a parked route.
		return promoteConflicts(ctx, tx)
	})
}

// DeleteRoute removes a ui or manual route.
func (s *Store) DeleteRoute(ctx context.Context, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM routes WHERE id = ? AND source != ?", id, SourceLabel)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return promoteConflicts(ctx, tx)
	})
}

// SyncLabelRoutes brings the label routes of one agent in line with
// desired. ApplyReport does the same as part of storing a report; this
// variant is for when the settings change.
func (s *Store) SyncLabelRoutes(ctx context.Context, agentID int64, desired []LabelRoute, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		return syncLabelRoutes(ctx, tx, agentID, desired, now)
	})
}

func syncLabelRoutes(ctx context.Context, tx *sql.Tx, agentID int64, desired []LabelRoute, now time.Time) error {
	existing, err := collect(ctx, tx, scanRoute,
		"SELECT "+routeColumns+" FROM routes WHERE source = ? AND agent_id = ?", SourceLabel, agentID)
	if err != nil {
		return err
	}
	byWorkload := make(map[string]Route, len(existing))
	for _, r := range existing {
		byWorkload[r.Workload] = r
	}

	for _, want := range desired {
		old, exists := byWorkload[want.Workload]
		delete(byWorkload, want.Workload) // whatever is left afterwards is obsolete

		// Same hostname as before and still valid: keep the row, and
		// with it the claim on the hostname, and only update details.
		if exists && want.Invalid == "" && old.State != StateInvalid && old.Hostname == want.Hostname {
			_, err := tx.ExecContext(ctx,
				"UPDATE routes SET container_port = ?, backend_https = ? WHERE id = ?",
				want.ContainerPort, want.BackendHTTPS, old.ID)
			if err != nil {
				return err
			}
			continue
		}

		// New route, or the hostname changed: start over. The new
		// hostname is claimed like by any newcomer.
		if exists {
			if _, err := tx.ExecContext(ctx, "DELETE FROM routes WHERE id = ?", old.ID); err != nil {
				return err
			}
		}
		route := Route{
			Source:        SourceLabel,
			Hostname:      want.Hostname,
			AgentID:       agentID,
			Workload:      want.Workload,
			ContainerPort: want.ContainerPort,
			BackendHTTPS:  want.BackendHTTPS,
			State:         StateOK,
			CreatedAt:     now,
		}
		if want.Invalid != "" {
			route.State, route.Detail = StateInvalid, want.Invalid
		} else {
			taken, err := hostnameTaken(ctx, tx, want.Hostname, 0)
			if err != nil {
				return err
			}
			if taken {
				// First come, first served: park the newcomer.
				route.State = StateConflict
			}
		}
		if _, err := insertRoute(ctx, tx, route); err != nil {
			return err
		}
	}

	// Workloads that no longer run or no longer carry the label.
	for _, old := range byWorkload {
		if _, err := tx.ExecContext(ctx, "DELETE FROM routes WHERE id = ?", old.ID); err != nil {
			return err
		}
	}
	return promoteConflicts(ctx, tx)
}

// promoteConflicts activates parked routes whose hostname has become
// free, oldest first. Call it after anything that can release a hostname.
func promoteConflicts(ctx context.Context, tx *sql.Tx) error {
	parked, err := collect(ctx, tx, scanRoute,
		"SELECT "+routeColumns+" FROM routes WHERE state = ? ORDER BY created_at, id", StateConflict)
	if err != nil {
		return err
	}
	for _, r := range parked {
		taken, err := hostnameTaken(ctx, tx, r.Hostname, r.ID)
		if err != nil {
			return err
		}
		if taken {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE routes SET state = ? WHERE id = ?", StateOK, r.ID); err != nil {
			return err
		}
	}
	return nil
}

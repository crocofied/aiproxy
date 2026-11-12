// Package store persists the controller's state in a SQLite database.
//
// It contains no business rules beyond data integrity: what a route
// should look like is decided in package routing, the store only keeps it.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	// The pure-Go SQLite driver registers itself under the name "sqlite".
	_ "modernc.org/sqlite"
)

// Errors that callers are expected to check with errors.Is.
var (
	ErrNotFound      = errors.New("not found")
	ErrHostnameTaken = errors.New("hostname is already in use")
	ErrNameTaken     = errors.New("name is already in use")
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is a handle to the database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (and if necessary creates) the database at path and brings
// its schema up to date.
func Open(ctx context.Context, path string) (*Store, error) {
	// Create the file ourselves so that it starts out with mode 0600.
	// SQLite gives its side files (-wal, -shm) the same permissions.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" + // readers do not block the writer
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)" + // off by default in SQLite
		"&_pragma=cache_size(-2000)" // 2 MB page cache keeps memory low
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection is plenty for this workload and rules out
	// "database is locked" errors between our own goroutines.
	//
	// Consequence: never run a query while iterating over the rows of
	// another one (outside a transaction that would wait forever for the
	// only connection). All list functions therefore read their rows
	// completely before doing anything else.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies all migration files whose number is higher than the
// schema version recorded in the database (PRAGMA user_version).
func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		// File names look like "migrations/001_init.sql".
		base := strings.TrimPrefix(name, "migrations/")
		number, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: name must start with a number", base)
		}
		if number <= current {
			continue
		}
		script, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(script)); err != nil {
				return err
			}
			// PRAGMA does not accept placeholders; number is an int.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", number))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %q: %w", base, err)
		}
	}
	return nil
}

// tx runs fn inside a transaction: commit if fn returns nil, roll back
// otherwise.
func (s *Store) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		// The original error is the interesting one.
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// querier is implemented by both *sql.DB and *sql.Tx, so helpers can be
// used inside and outside of transactions.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// collect runs a query and returns all rows, converted by scan. Reading
// everything before returning releases the connection (see Open).
func collect[T any](ctx context.Context, q querier, scan func(*sql.Rows) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

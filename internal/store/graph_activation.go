package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
)

// GraphActiveVersionConflict is a failed compare-and-swap. Its observed head
// belongs to the comparison transaction and may change after the response.
type GraphActiveVersionConflict struct {
	Namespace string
	Expected  *string
	Active    *string
}

func (e *GraphActiveVersionConflict) Error() string { return "graph active version conflict" }

// ActivateGraphSnapshot preserves the original single-writer operation.
func (s *Store) ActivateGraphSnapshot(ctx context.Context, namespace, version string) (bool, error) {
	return s.activateGraphSnapshot(ctx, namespace, version, false, nil)
}

// ActivateGraphSnapshotConditional requires an exact current head match. A
// nil expectation means no active snapshot. Comparison precedes idempotence:
// replaying an obsolete expectation conflicts even if the target is active.
func (s *Store) ActivateGraphSnapshotConditional(ctx context.Context, namespace, version string, expected *string) (bool, error) {
	return s.activateGraphSnapshot(ctx, namespace, version, true, expected)
}

func (s *Store) activateGraphSnapshot(ctx context.Context, namespace, version string, conditional bool, expected *string) (bool, error) {
	if err := s.GraphUnavailable(); err != nil {
		return false, err
	}
	if namespace == "" || version == "" || (expected != nil && *expected == "") {
		return false, ErrInvalidGraphIdentity
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// SQLite pragmas are connection-local. Reserve the write lock before reads
	// so concurrent processes observe a serialized head instead of SQLITE_BUSY.
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return false, err
	}
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return false, err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if err = namespaceRequireActive(ctx, conn, namespace); err != nil {
		return false, err
	}
	var ready int
	if err = conn.QueryRowContext(ctx, `SELECT query_ready FROM graph_snapshots WHERE namespace=? AND version=?`, namespace, version).Scan(&ready); errors.Is(err, sql.ErrNoRows) {
		return false, ErrGraphSnapshotNotFound
	} else if err != nil {
		return false, err
	}
	if ready != 1 {
		return false, fmt.Errorf("%w: %s/%s", ErrGraphSnapshotNotReady, namespace, version)
	}
	var current string
	err = conn.QueryRowContext(ctx, `SELECT active_version FROM graph_namespace_heads WHERE namespace=?`, namespace).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if conditional && ((expected == nil && current != "") || (expected != nil && *expected != current)) {
		return false, &GraphActiveVersionConflict{Namespace: namespace, Expected: expected, Active: stringPointer(current)}
	}
	changed := current != version
	if changed {
		if _, err = conn.ExecContext(ctx, `INSERT INTO graph_namespace_heads(namespace,active_version) VALUES(?,?) ON CONFLICT(namespace) DO UPDATE SET active_version=excluded.active_version`, namespace, version); err != nil {
			return false, err
		}
	}
	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return false, err
	}
	// Release this connection before retention obtains another. A committed
	// pointer remains authoritative even if post-commit retention fails.
	if err = conn.Close(); err != nil {
		return false, err
	}
	if changed {
		if err = s.ApplyGraphRetrievalRetention(ctx, namespace); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// LookupActiveGraphSnapshot reads head and hash in one statement. Snapshot
// existence or readiness alone never implies that it has been published.
func (s *Store) LookupActiveGraphSnapshot(ctx context.Context, namespace string) (graphsnapshot.ActiveSnapshot, error) {
	result := graphsnapshot.ActiveSnapshot{Namespace: namespace}
	if err := s.GraphUnavailable(); err != nil {
		return result, err
	}
	if namespace == "" {
		return result, ErrInvalidGraphIdentity
	}
	var version, hash string
	err := s.db.QueryRowContext(ctx, `SELECT head.active_version,snapshot.content_hash FROM graph_namespace_heads AS head JOIN graph_snapshots AS snapshot ON snapshot.namespace=head.namespace AND snapshot.version=head.active_version WHERE head.namespace=?`, namespace).Scan(&version, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.ActiveVersion, result.ContentHash = &version, &hash
	return result, nil
}

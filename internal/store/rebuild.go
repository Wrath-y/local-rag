package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ChunkSnapshot is the immutable chunk data used by one index rebuild run.
type ChunkSnapshot struct {
	ID   int64
	Text string
}

// SnapshotChunks returns a transactionally consistent view of all chunk IDs and
// text. Callers can embed it without retaining a database read transaction.
func (s *Store) SnapshotChunks() ([]ChunkSnapshot, error) {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin chunk snapshot: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, text FROM chunks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query chunk snapshot: %w", err)
	}
	defer rows.Close()

	var snapshot []ChunkSnapshot
	for rows.Next() {
		var chunk ChunkSnapshot
		if err := rows.Scan(&chunk.ID, &chunk.Text); err != nil {
			return nil, fmt.Errorf("scan chunk snapshot: %w", err)
		}
		snapshot = append(snapshot, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit chunk snapshot: %w", err)
	}
	return snapshot, nil
}

// CreateShadowIndex creates a vec0 table that is isolated from the active
// vec_chunks retrieval table.
func (s *Store) CreateShadowIndex(name string) error {
	if err := validateIndexName(name); err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE %s USING vec0(chunk_id INTEGER PRIMARY KEY, embedding float[%d])`, quoteIdent(name), s.dims)); err != nil {
		return fmt.Errorf("create shadow index: %w", err)
	}
	return nil
}

// DropIndex removes an uncommitted shadow index. It is safe to call during
// cleanup after a failed build.
func (s *Store) DropIndex(name string) error {
	if err := validateIndexName(name); err != nil {
		return err
	}
	_, err := s.db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quoteIdent(name)))
	return err
}

// InsertShadowVectors writes one embedding batch into an isolated shadow table.
func (s *Store) InsertShadowVectors(name string, chunks []ChunkSnapshot, vectors [][]float32) error {
	if err := validateIndexName(name); err != nil {
		return err
	}
	if len(chunks) != len(vectors) {
		return fmt.Errorf("shadow vector count mismatch: chunks=%d vectors=%d", len(chunks), len(vectors))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin shadow insert: %w", err)
	}
	defer tx.Rollback()
	statement := fmt.Sprintf(`INSERT INTO %s (chunk_id, embedding) VALUES (?, ?)`, quoteIdent(name))
	for i, vector := range vectors {
		if len(vector) != s.dims {
			return fmt.Errorf("shadow vector %d has dimension %d, want %d", i, len(vector), s.dims)
		}
		if _, err := tx.Exec(statement, chunks[i].ID, Float32ToBytes(vector)); err != nil {
			return fmt.Errorf("insert shadow vector %d: %w", chunks[i].ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit shadow insert: %w", err)
	}
	return nil
}

// ValidateShadowIndex verifies the cardinality, ID set, vector dimensions, and
// one representative nearest-neighbour lookup before it can become active.
func (s *Store) ValidateShadowIndex(name string, snapshot []ChunkSnapshot) error {
	if err := validateIndexName(name); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quoteIdent(name))).Scan(&count); err != nil {
		return fmt.Errorf("count shadow index: %w", err)
	}
	if count != len(snapshot) {
		return fmt.Errorf("shadow count %d does not match snapshot count %d", count, len(snapshot))
	}
	expected := make(map[int64]struct{}, len(snapshot))
	knownIDs := make(map[int64]struct{}, len(snapshot))
	for _, chunk := range snapshot {
		expected[chunk.ID] = struct{}{}
		knownIDs[chunk.ID] = struct{}{}
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT chunk_id, embedding FROM %s ORDER BY chunk_id`, quoteIdent(name)))
	if err != nil {
		return fmt.Errorf("query shadow vectors: %w", err)
	}
	defer rows.Close()
	var sampleVector []byte
	for rows.Next() {
		var id int64
		var vector []byte
		if err := rows.Scan(&id, &vector); err != nil {
			return fmt.Errorf("scan shadow vector: %w", err)
		}
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("shadow contains unexpected chunk ID %d", id)
		}
		delete(expected, id)
		if len(vector) != s.dims*4 {
			return fmt.Errorf("shadow vector %d has byte length %d, want %d", id, len(vector), s.dims*4)
		}
		if sampleVector == nil {
			sampleVector = vector
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate shadow vectors: %w", err)
	}
	if len(expected) != 0 {
		return fmt.Errorf("shadow index is missing %d chunk IDs", len(expected))
	}
	if sampleVector != nil {
		var nearestID int64
		err := s.db.QueryRow(fmt.Sprintf(`SELECT chunk_id FROM %s WHERE embedding MATCH ? ORDER BY distance LIMIT 1`, quoteIdent(name)), sampleVector).Scan(&nearestID)
		if err != nil {
			return fmt.Errorf("representative shadow retrieval: %w", err)
		}
		if _, ok := knownIDs[nearestID]; !ok {
			return fmt.Errorf("representative shadow retrieval returned unexpected chunk ID %d", nearestID)
		}
	}
	return nil
}

// ActivateShadowIndex atomically replaces vec_chunks with a validated shadow
// table and retains a copy of the previous active table under retiredName.
// sqlite-vec virtual tables cannot safely be renamed with the version used by
// this service, so the fixed active table is replaced transactionally instead.
func (s *Store) ActivateShadowIndex(shadowName, retiredName string) error {
	if err := validateIndexName(shadowName); err != nil {
		return err
	}
	if err := validateIndexName(retiredName); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin index cutover: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE %s USING vec0(chunk_id INTEGER PRIMARY KEY, embedding float[%d])`, quoteIdent(retiredName), s.dims)); err != nil {
		return fmt.Errorf("create retained active index: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s (chunk_id, embedding) SELECT chunk_id, embedding FROM vec_chunks`, quoteIdent(retiredName))); err != nil {
		return fmt.Errorf("retain active index: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM vec_chunks`); err != nil {
		return fmt.Errorf("clear active index: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO vec_chunks (chunk_id, embedding) SELECT chunk_id, embedding FROM %s`, quoteIdent(shadowName))); err != nil {
		return fmt.Errorf("activate shadow index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit index cutover: %w", err)
	}
	return nil
}

// RollbackActiveIndex restores a retained active index after a post-cutover
// health check fails. The failed replacement is retained briefly then removed.
func (s *Store) RollbackActiveIndex(retiredName, failedName string) error {
	if err := validateIndexName(retiredName); err != nil {
		return err
	}
	if err := validateIndexName(failedName); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin index rollback: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE %s USING vec0(chunk_id INTEGER PRIMARY KEY, embedding float[%d])`, quoteIdent(failedName), s.dims)); err != nil {
		return fmt.Errorf("create failed replacement backup: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s (chunk_id, embedding) SELECT chunk_id, embedding FROM vec_chunks`, quoteIdent(failedName))); err != nil {
		return fmt.Errorf("retain failed replacement: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM vec_chunks`); err != nil {
		return fmt.Errorf("clear failed replacement: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO vec_chunks (chunk_id, embedding) SELECT chunk_id, embedding FROM %s`, quoteIdent(retiredName))); err != nil {
		return fmt.Errorf("restore retained index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit index rollback: %w", err)
	}
	if err := s.DropIndex(failedName); err != nil {
		return fmt.Errorf("drop failed replacement: %w", err)
	}
	return nil
}

func validateIndexName(name string) error {
	if name == "vec_chunks" || (strings.HasPrefix(name, "vec_chunks_") && len(name) <= 128) {
		for _, r := range name {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
				return fmt.Errorf("invalid index name %q", name)
			}
		}
		return nil
	}
	return fmt.Errorf("invalid index name %q", name)
}

func quoteIdent(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

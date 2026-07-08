package store

import "fmt"

// SourceInfo holds a source name and its chunk count.
type SourceInfo struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// ListSources returns all sources with their chunk counts, ordered by source name.
func (s *Store) ListSources() ([]SourceInfo, error) {
	rows, err := s.db.Query(
		`SELECT source, COUNT(*) FROM chunks GROUP BY source ORDER BY source`,
	)
	if err != nil {
		return nil, fmt.Errorf("ListSources: %w", err)
	}
	defer rows.Close()

	var infos []SourceInfo
	for rows.Next() {
		var info SourceInfo
		if err := rows.Scan(&info.Source, &info.Count); err != nil {
			return nil, fmt.Errorf("ListSources scan: %w", err)
		}
		infos = append(infos, info)
	}
	return infos, rows.Err()
}

// DeleteSource removes all chunks (and their vector entries) for a given source.
// Returns the number of chunks deleted.
// The FTS5 trigger (chunks_ad) handles chunks_fts cleanup automatically.
func (s *Store) DeleteSource(source string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("DeleteSource begin: %w", err)
	}
	defer tx.Rollback()

	// 1. Collect IDs for the source.
	rows, err := tx.Query(`SELECT id FROM chunks WHERE source = ?`, source)
	if err != nil {
		return 0, fmt.Errorf("DeleteSource select ids: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("DeleteSource scan id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("DeleteSource rows: %w", err)
	}

	// 2. Delete vector entries for each id.
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM vec_chunks WHERE chunk_id = ?`, id); err != nil {
			return 0, fmt.Errorf("DeleteSource delete vec_chunks id=%d: %w", id, err)
		}
	}

	// 3. Delete chunks rows (triggers chunks_ad → FTS5 cleanup).
	res, err := tx.Exec(`DELETE FROM chunks WHERE source = ?`, source)
	if err != nil {
		return 0, fmt.Errorf("DeleteSource delete chunks: %w", err)
	}
	n, _ := res.RowsAffected()

	return int(n), tx.Commit()
}

// Reset removes ALL data: chunks, vec_chunks, and the FTS5 index.
func (s *Store) Reset() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("Reset begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM vec_chunks`); err != nil {
		return fmt.Errorf("Reset delete vec_chunks: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM chunks`); err != nil {
		return fmt.Errorf("Reset delete chunks: %w", err)
	}
	// Rebuild FTS5 index to sync with the now-empty content table.
	if _, err := tx.Exec(`INSERT INTO chunks_fts(chunks_fts) VALUES ('rebuild')`); err != nil {
		return fmt.Errorf("Reset rebuild fts: %w", err)
	}

	return tx.Commit()
}

// Stats returns basic statistics about the store.
func (s *Store) Stats() (map[string]interface{}, error) {
	count, err := s.ChunkCount()
	if err != nil {
		return nil, fmt.Errorf("Stats: %w", err)
	}
	return map[string]interface{}{
		"total_chunks": count,
	}, nil
}

// IntegrityCheck runs SQLite's integrity_check PRAGMA.
// Returns "ok" if the database is clean.
func (s *Store) IntegrityCheck() (string, error) {
	var result string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return "", fmt.Errorf("IntegrityCheck: %w", err)
	}
	return result, nil
}

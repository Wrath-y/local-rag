package store

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// Provenance contains optional document-level citation metadata. URI is the
// original URI or filesystem path; Location identifies a position in it.
type Provenance struct {
	Title    string
	URI      string
	Location string
	// ConnectorMetadata is a validated, schema-versioned JSON envelope. It is
	// nullable so chunks created by older releases remain readable.
	ConnectorMetadata string
}

// InsertChunk inserts a chunk with its embedding vector.
// Returns (id, nil) on success, (0, nil) if md5 duplicate (skip), or (0, err) on failure.
func (s *Store) InsertChunk(text, source, md5, parentText, parentID string, embedding []float32) (int64, error) {
	return s.InsertChunkWithProvenance(text, source, md5, parentText, parentID, Provenance{}, embedding)
}

// InsertChunkWithProvenance inserts a chunk and its optional citation
// provenance. InsertChunk remains available for callers that do not have it.
func (s *Store) InsertChunkWithProvenance(text, source, md5, parentText, parentID string, provenance Provenance, embedding []float32) (int64, error) {
	// 1. Check if md5 already exists
	var exists int
	err := s.db.QueryRow("SELECT 1 FROM chunks WHERE md5 = ?", md5).Scan(&exists)
	if err == nil {
		return 0, nil // duplicate
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("check duplicate: %w", err)
	}

	// 2. Begin transaction
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 3. Insert into chunks table
	res, err := tx.Exec(
		"INSERT INTO chunks (text, source, md5, parent_text, parent_id, document_title, document_uri, location, connector_metadata, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		text, source, md5, parentText, parentID, provenance.Title, provenance.URI, provenance.Location, provenance.ConnectorMetadata, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("insert chunk: %w", err)
	}
	id, _ := res.LastInsertId()

	// 4. Insert into vec_chunks (sqlite-vec)
	vecBytes := Float32ToBytes(embedding)
	if _, err := tx.Exec("INSERT INTO vec_chunks (chunk_id, embedding) VALUES (?, ?)", id, vecBytes); err != nil {
		return 0, fmt.Errorf("insert vec: %w", err)
	}

	// 5. Commit
	return id, tx.Commit()
}

// ChunkCount returns total number of chunks.
func (s *Store) ChunkCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count)
	return count, err
}

// Float32ToBytes converts []float32 to little-endian bytes for sqlite-vec.
func Float32ToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

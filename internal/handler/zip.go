package handler

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const (
	BackupPackageFormatVersion = 1
	BackupPackageSchemaVersion = 1
	BackupDatabaseEntryName    = "rag.db"
	BackupManifestEntryName    = "manifest.json"
)

// EmbeddingSummary describes the embedding configuration required by a backup.
type EmbeddingSummary struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

// BackupFile describes a protected data file stored in a backup archive.
type BackupFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// BackupManifest describes a versioned backup package.
type BackupManifest struct {
	FormatVersion int              `json:"format_version"`
	SchemaVersion int              `json:"schema_version"`
	CreatedAt     string           `json:"created_at"`
	ChunkCount    int              `json:"chunk_count"`
	Embedding     EmbeddingSummary `json:"embedding"`
	Files         []BackupFile     `json:"files"`
}

// BackupPackageMetadata contains runtime data included in a backup manifest.
type BackupPackageMetadata struct {
	ChunkCount int
	Embedding  EmbeddingSummary
}

// createDBZip reads dbPath and returns a versioned backup package.
func createDBZip(dbPath string, metadata BackupPackageMetadata) ([]byte, error) {
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("read db: %w", err)
	}

	sum := sha256.Sum256(data)
	manifest := BackupManifest{
		FormatVersion: BackupPackageFormatVersion,
		SchemaVersion: BackupPackageSchemaVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		ChunkCount:    metadata.ChunkCount,
		Embedding:     metadata.Embedding,
		Files: []BackupFile{{
			Path:      BackupDatabaseEntryName,
			SizeBytes: int64(len(data)),
			SHA256:    hex.EncodeToString(sum[:]),
		}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	if err := writeZipEntry(w, BackupDatabaseEntryName, data); err != nil {
		return nil, err
	}
	if err := writeZipEntry(w, BackupManifestEntryName, manifestData); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

func writeZipEntry(w *zip.Writer, name string, data []byte) error {
	fw, err := w.Create(name)
	if err != nil {
		return fmt.Errorf("create zip entry %q: %w", name, err)
	}
	if _, err := fw.Write(data); err != nil {
		return fmt.Errorf("write zip entry %q: %w", name, err)
	}
	return nil
}

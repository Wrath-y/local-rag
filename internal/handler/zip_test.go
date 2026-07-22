package handler

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateDBZip_WritesVersionedManifest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	dbData := []byte("SQLite format 3\x00test database")
	if err := os.WriteFile(dbPath, dbData, 0o600); err != nil {
		t.Fatal(err)
	}

	zipData, err := createDBZip(dbPath, BackupPackageMetadata{
		ChunkCount: 7,
		Embedding: EmbeddingSummary{
			Provider:   "test-provider",
			Model:      "test-model",
			Dimensions: 4,
		},
	})
	if err != nil {
		t.Fatalf("createDBZip: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	if len(reader.File) != 2 {
		t.Fatalf("expected exactly database and manifest entries, got %d", len(reader.File))
	}

	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		body, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		data := new(bytes.Buffer)
		if _, err := data.ReadFrom(body); err != nil {
			_ = body.Close()
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if err := body.Close(); err != nil {
			t.Fatalf("close %s: %v", file.Name, err)
		}
		entries[file.Name] = data.Bytes()
	}

	if got := entries[BackupDatabaseEntryName]; !bytes.Equal(got, dbData) {
		t.Errorf("database entry = %q, want %q", got, dbData)
	}

	var manifest BackupManifest
	if err := json.Unmarshal(entries[BackupManifestEntryName], &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.FormatVersion != BackupPackageFormatVersion {
		t.Errorf("format version = %d, want %d", manifest.FormatVersion, BackupPackageFormatVersion)
	}
	if manifest.SchemaVersion != BackupPackageSchemaVersion {
		t.Errorf("schema version = %d, want %d", manifest.SchemaVersion, BackupPackageSchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, manifest.CreatedAt); err != nil {
		t.Errorf("created_at = %q is not RFC3339: %v", manifest.CreatedAt, err)
	}
	if manifest.ChunkCount != 7 {
		t.Errorf("chunk_count = %d, want 7", manifest.ChunkCount)
	}
	if manifest.Embedding != (EmbeddingSummary{Provider: "test-provider", Model: "test-model", Dimensions: 4}) {
		t.Errorf("embedding = %#v", manifest.Embedding)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("manifest files = %d, want 1", len(manifest.Files))
	}

	file := manifest.Files[0]
	if file.Path != BackupDatabaseEntryName {
		t.Errorf("file path = %q, want %q", file.Path, BackupDatabaseEntryName)
	}
	if file.SizeBytes != int64(len(dbData)) {
		t.Errorf("file size = %d, want %d", file.SizeBytes, len(dbData))
	}
	sum := sha256.Sum256(dbData)
	if file.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("file checksum = %q, want %q", file.SHA256, hex.EncodeToString(sum[:]))
	}
}

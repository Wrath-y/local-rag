package handler

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

func TestValidateAndExtractBackup_ValidPackage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "active.db")
	st, err := store.New(dbPath, 4)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	deps := testDeps(t, st)
	deps.Config.Storage.DBPath = dbPath
	deps.Config.Embedding.Provider = "test-provider"
	deps.Config.Embedding.Model = "test-model"
	deps.Config.Embedding.Dims = 4
	h := New(deps)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/export", nil)
	h.Export(c)
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", w.Code, w.Body.String())
	}

	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(archivePath, w.Body.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	validated, cleanup, err := ValidateAndExtractBackup(archivePath, t.TempDir(), DefaultBackupValidationLimits)
	if err != nil {
		t.Fatalf("validate archive: %v", err)
	}
	defer cleanup()
	if validated.Manifest.FormatVersion != BackupPackageFormatVersion {
		t.Errorf("format version = %d", validated.Manifest.FormatVersion)
	}
	if validated.DatabasePath == "" {
		t.Error("database path is empty")
	}
	if _, err := os.Stat(validated.DatabasePath); err != nil {
		t.Errorf("extracted database unavailable: %v", err)
	}
}

func createLegacyDBZip(data []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create(BackupDatabaseEntryName)
	if err != nil {
		return nil, err
	}
	if _, err := entry.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func TestValidateAndExtractBackup_RejectsInvalidManifestAndLimits(t *testing.T) {
	validDatabase := validDatabaseBytes(t)
	validManifest := manifestForDatabase(validDatabase)
	cases := []struct {
		name    string
		entries []zipEntry
		limits  BackupValidationLimits
	}{
		{
			name:    "tampered database",
			entries: []zipEntry{{name: BackupManifestEntryName, data: validManifest}, {name: BackupDatabaseEntryName, data: append(validDatabase, 'x')}},
			limits:  DefaultBackupValidationLimits,
		},
		{
			name:    "invalid manifest JSON",
			entries: []zipEntry{{name: BackupManifestEntryName, data: []byte("{")}, {name: BackupDatabaseEntryName, data: validDatabase}},
			limits:  DefaultBackupValidationLimits,
		},
		{
			name:    "incompatible version",
			entries: []zipEntry{{name: BackupManifestEntryName, data: []byte(`{"format_version":99,"schema_version":1,"created_at":"2026-01-01T00:00:00Z","chunk_count":0,"embedding":{"provider":"test","model":"test","dimensions":4},"files":[{"path":"rag.db","size_bytes":1,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`)}, {name: BackupDatabaseEntryName, data: validDatabase}},
			limits:  DefaultBackupValidationLimits,
		},
		{
			name:    "entry count over limit",
			entries: []zipEntry{{name: BackupManifestEntryName, data: validManifest}, {name: BackupDatabaseEntryName, data: validDatabase}, {name: "extra", data: []byte("x")}},
			limits:  BackupValidationLimits{MaxArchiveBytes: 1 << 20, MaxEntries: 2, MaxCompressedBytes: 1 << 20, MaxUncompressedBytes: 1 << 20, MaxFileBytes: 1 << 20},
		},
		{
			name:    "database over file limit",
			entries: []zipEntry{{name: BackupManifestEntryName, data: validManifest}, {name: BackupDatabaseEntryName, data: validDatabase}},
			limits:  BackupValidationLimits{MaxArchiveBytes: 1 << 20, MaxEntries: 2, MaxCompressedBytes: 1 << 20, MaxUncompressedBytes: 1 << 20, MaxFileBytes: 1},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "invalid.zip")
			if err := os.WriteFile(archivePath, makeZipEntries(t, test.entries), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ValidateAndExtractBackup(archivePath, t.TempDir(), test.limits); err == nil {
				t.Fatal("expected archive rejection")
			}
		})
	}
}

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func makeZipEntries(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		if item.mode != 0 {
			header.SetMode(item.mode)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func validDatabaseBytes(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "valid.db")
	db, err := store.New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func manifestForDatabase(data []byte) []byte {
	sum := sha256.Sum256(data)
	manifest := BackupManifest{FormatVersion: BackupPackageFormatVersion, SchemaVersion: BackupPackageSchemaVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339), ChunkCount: 0, Embedding: EmbeddingSummary{Provider: "test", Model: "test", Dimensions: 4}, Files: []BackupFile{{Path: BackupDatabaseEntryName, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	encoded, _ := json.Marshal(manifest)
	return encoded
}

func TestValidateAndExtractBackup_RejectsUnsafeArchives(t *testing.T) {
	cases := []struct {
		name string
		zip  func(t *testing.T) []byte
	}{
		{
			name: "traversal entry",
			zip: func(t *testing.T) []byte {
				t.Helper()
				return makeZip(t, map[string][]byte{"../outside.db": []byte("data")})
			},
		},
		{
			name: "unexpected entry",
			zip: func(t *testing.T) []byte {
				t.Helper()
				return makeZip(t, map[string][]byte{"unexpected": []byte("data")})
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "invalid.zip")
			if err := os.WriteFile(archivePath, test.zip(t), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ValidateAndExtractBackup(archivePath, t.TempDir(), DefaultBackupValidationLimits); err == nil {
				t.Fatal("expected invalid archive rejection")
			}
		})
	}
}

func makeZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, data := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestValidateAndExtractBackup_RejectsManifestlessArchive(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "legacy.zip")
	zipData, err := createLegacyDBZip([]byte("not a database"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, zipData, 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err = ValidateAndExtractBackup(archivePath, t.TempDir(), DefaultBackupValidationLimits)
	if err == nil {
		t.Fatal("expected manifestless archive rejection")
	}
}

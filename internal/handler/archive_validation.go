package handler

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const maxManifestBytes int64 = 1 << 20

// BackupValidationLimits bounds archive inspection and extraction.
type BackupValidationLimits struct {
	MaxArchiveBytes      int64
	MaxEntries           int
	MaxCompressedBytes   int64
	MaxUncompressedBytes int64
	MaxFileBytes         int64
}

// DefaultBackupValidationLimits protects local imports from oversized archives.
var DefaultBackupValidationLimits = BackupValidationLimits{
	MaxArchiveBytes:      256 << 20,
	MaxEntries:           2,
	MaxCompressedBytes:   256 << 20,
	MaxUncompressedBytes: 512 << 20,
	MaxFileBytes:         512 << 20,
}

// ValidatedBackup is an extracted, verified backup package ready for restore.
type ValidatedBackup struct {
	Manifest     BackupManifest
	Directory    string
	DatabasePath string
}

// ValidateAndExtractBackup verifies an archive and extracts its database into
// a newly created directory under temporaryParent. The caller owns cleanup.
func ValidateAndExtractBackup(archivePath, temporaryParent string, limits BackupValidationLimits) (*ValidatedBackup, func(), error) {
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("stat archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limits.MaxArchiveBytes {
		return nil, nil, fmt.Errorf("archive size is invalid or exceeds %d bytes", limits.MaxArchiveBytes)
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) != limits.MaxEntries {
		return nil, nil, fmt.Errorf("archive must contain exactly %d entries", limits.MaxEntries)
	}

	entries := make(map[string]*zip.File, len(reader.File))
	var compressedTotal, uncompressedTotal uint64
	for _, file := range reader.File {
		if err := validateArchiveEntry(file); err != nil {
			return nil, nil, err
		}
		if _, exists := entries[file.Name]; exists {
			return nil, nil, fmt.Errorf("archive contains duplicate entry %q", file.Name)
		}
		compressedTotal += file.CompressedSize64
		uncompressedTotal += file.UncompressedSize64
		if compressedTotal > uint64(limits.MaxCompressedBytes) || uncompressedTotal > uint64(limits.MaxUncompressedBytes) || file.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			return nil, nil, fmt.Errorf("archive entries exceed extraction limits")
		}
		entries[file.Name] = file
	}

	manifestFile, ok := entries[BackupManifestEntryName]
	if !ok {
		return nil, nil, fmt.Errorf("archive has no manifest.json; re-export the knowledge base with the current version")
	}
	manifest, err := readAndValidateManifest(manifestFile, limits)
	if err != nil {
		return nil, nil, err
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != BackupDatabaseEntryName {
		return nil, nil, fmt.Errorf("manifest must describe exactly %q", BackupDatabaseEntryName)
	}
	if len(entries) != 2 || entries[BackupDatabaseEntryName] == nil {
		return nil, nil, fmt.Errorf("archive entries do not match manifest")
	}

	directory, err := os.MkdirTemp(temporaryParent, ".rag-import-")
	if err != nil {
		return nil, nil, fmt.Errorf("create extraction directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	databasePath := filepath.Join(directory, BackupDatabaseEntryName)
	if err := extractAndVerify(entries[BackupDatabaseEntryName], databasePath, manifest.Files[0], limits); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := validateDatabase(databasePath); err != nil {
		cleanup()
		return nil, nil, err
	}
	return &ValidatedBackup{Manifest: manifest, Directory: directory, DatabasePath: databasePath}, cleanup, nil
}

func validateArchiveEntry(file *zip.File) error {
	if file.Name == "" || strings.Contains(file.Name, "\\") || filepath.IsAbs(file.Name) || strings.HasPrefix(file.Name, "/") || filepath.Clean(file.Name) != file.Name || strings.Contains(file.Name, "..") {
		return fmt.Errorf("archive entry %q has an unsafe path", file.Name)
	}
	if file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
		return fmt.Errorf("archive entry %q is not a regular file", file.Name)
	}
	return nil
}

func readAndValidateManifest(file *zip.File, limits BackupValidationLimits) (BackupManifest, error) {
	if file.UncompressedSize64 > uint64(maxManifestBytes) {
		return BackupManifest{}, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	body, err := file.Open()
	if err != nil {
		return BackupManifest{}, fmt.Errorf("open manifest: %w", err)
	}
	defer body.Close()
	decoder := json.NewDecoder(io.LimitReader(body, maxManifestBytes+1))
	var manifest BackupManifest
	if err := decoder.Decode(&manifest); err != nil {
		return BackupManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return BackupManifest{}, fmt.Errorf("manifest must contain one JSON value")
	}
	if manifest.FormatVersion != BackupPackageFormatVersion || manifest.SchemaVersion != BackupPackageSchemaVersion {
		return BackupManifest{}, fmt.Errorf("unsupported backup format/schema version %d/%d", manifest.FormatVersion, manifest.SchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, manifest.CreatedAt); err != nil || manifest.ChunkCount < 0 || manifest.Embedding.Provider == "" || manifest.Embedding.Model == "" || manifest.Embedding.Dimensions <= 0 {
		return BackupManifest{}, fmt.Errorf("manifest contains invalid metadata")
	}
	if len(manifest.Files) != 1 || manifest.Files[0].SizeBytes <= 0 || manifest.Files[0].SizeBytes > limits.MaxFileBytes {
		return BackupManifest{}, fmt.Errorf("manifest contains invalid protected files")
	}
	if _, err := hex.DecodeString(manifest.Files[0].SHA256); err != nil || len(manifest.Files[0].SHA256) != sha256.Size*2 {
		return BackupManifest{}, fmt.Errorf("manifest contains invalid sha256")
	}
	return manifest, nil
}

func extractAndVerify(file *zip.File, destination string, metadata BackupFile, limits BackupValidationLimits) error {
	body, err := file.Open()
	if err != nil {
		return fmt.Errorf("open database entry: %w", err)
	}
	defer body.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create extracted database: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(body, limits.MaxFileBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("extract database: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close extracted database: %w", closeErr)
	}
	if written > limits.MaxFileBytes || written != metadata.SizeBytes {
		return fmt.Errorf("database size does not match manifest")
	}
	if hex.EncodeToString(hash.Sum(nil)) != metadata.SHA256 {
		return fmt.Errorf("database checksum does not match manifest")
	}
	return nil
}

func validateDatabase(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open extracted database: %w", err)
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		if err != nil {
			return fmt.Errorf("validate extracted database: %w", err)
		}
		return fmt.Errorf("validate extracted database: integrity check returned %q", integrity)
	}
	if err := validateDatabaseSchema(db); err != nil {
		return fmt.Errorf("validate extracted database schema: %w", err)
	}
	return nil
}

func validateDatabaseSchema(db *sql.DB) error {
	requiredColumns := map[string]bool{
		"id": false, "text": false, "source": false, "md5": false,
		"parent_text": false, "parent_id": false, "created_at": false,
	}
	rows, err := db.Query("PRAGMA table_info(chunks)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if _, required := requiredColumns[name]; required {
			requiredColumns[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, present := range requiredColumns {
		if !present {
			return fmt.Errorf("chunks.%s is missing", name)
		}
	}

	definitions := map[string]string{}
	rows, err = db.Query("SELECT name, sql FROM sqlite_master WHERE name IN ('vec_chunks', 'chunks_fts', 'chunks_ai', 'chunks_ad')")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			return err
		}
		definitions[name] = strings.ToLower(definition)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, requirement := range map[string]string{
		"vec_chunks": "using vec0",
		"chunks_fts": "using fts5",
		"chunks_ai":  "after insert on chunks",
		"chunks_ad":  "after delete on chunks",
	} {
		if !strings.Contains(definitions[name], requirement) {
			return fmt.Errorf("%s is missing required definition", name)
		}
	}
	return nil
}

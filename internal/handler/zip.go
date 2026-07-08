package handler

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// createDBZip reads dbPath and returns a zip archive containing the file.
func createDBZip(dbPath string) ([]byte, error) {
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("read db: %w", err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	fw, err := w.Create(filepath.Base(dbPath))
	if err != nil {
		return nil, fmt.Errorf("create zip entry: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return nil, fmt.Errorf("write zip entry: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

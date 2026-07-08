package handler

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

// extractFirstFileFromZip reads the first file entry in a zip archive.
func extractFirstFileFromZip(data []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	if len(r.File) == 0 {
		return nil, fmt.Errorf("empty zip archive")
	}
	f, err := r.File[0].Open()
	if err != nil {
		return nil, fmt.Errorf("open entry: %w", err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

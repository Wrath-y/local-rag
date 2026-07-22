package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// BackupRun creates an immediate backup of the database file.
func (h *Handler) BackupRun(c *gin.Context) {
	dbPath := h.deps.Config.Storage.DBPath

	backupDir := filepath.Join(filepath.Dir(dbPath), "..", "backups",
		time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mkdir: " + err.Error()})
		return
	}

	zipName := fmt.Sprintf("rag-%s.zip", time.Now().Format("150405"))
	zipPath := filepath.Join(backupDir, zipName)

	data, err := createDBZip(dbPath, h.backupPackageMetadata())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write backup: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"path":       zipPath,
		"size_bytes": len(data),
	})
}

// BackupList lists available backup zip files, newest first.
func (h *Handler) BackupList(c *gin.Context) {
	dbPath := h.deps.Config.Storage.DBPath
	backupsDir := filepath.Join(filepath.Dir(dbPath), "..", "backups")

	var files []map[string]interface{}

	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusOK, gin.H{"backups": files})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type backupEntry struct {
		name    string
		modTime time.Time
		size    int64
		path    string
	}
	var bkups []backupEntry

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dayDir := filepath.Join(backupsDir, entry.Name())
		dayEntries, err := os.ReadDir(dayDir)
		if err != nil {
			continue
		}
		for _, f := range dayEntries {
			if filepath.Ext(f.Name()) != ".zip" {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			bkups = append(bkups, backupEntry{
				name:    f.Name(),
				modTime: fi.ModTime(),
				size:    fi.Size(),
				path:    filepath.Join(dayDir, f.Name()),
			})
		}
	}

	// Sort newest first.
	sort.Slice(bkups, func(i, j int) bool {
		return bkups[i].modTime.After(bkups[j].modTime)
	})

	for _, b := range bkups {
		files = append(files, map[string]interface{}{
			"name":     b.name,
			"path":     b.path,
			"size":     b.size,
			"modified": b.modTime.Format(time.RFC3339),
		})
	}
	if files == nil {
		files = []map[string]interface{}{}
	}
	c.JSON(http.StatusOK, gin.H{"backups": files})
}

type restoreRequest struct {
	File    string `json:"file" binding:"required"`
	Confirm bool   `json:"confirm"`
}

// BackupRestore restores a confirmed project backup through RestoreService.
func (h *Handler) BackupRestore(c *gin.Context) {
	var req restoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be true"})
		return
	}
	if info, err := os.Stat(req.File); err != nil || !info.Mode().IsRegular() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backup file not found or invalid"})
		return
	}
	result, err := h.deps.Restore.Restore(req.File)
	if err != nil {
		status := http.StatusInternalServerError
		if result.Stage == RestoreStageValidate {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"status": "failed", "stage": result.Stage, "rolled_back": result.RolledBack, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": result.Stage, "snapshot_path": result.SnapshotPath})
}

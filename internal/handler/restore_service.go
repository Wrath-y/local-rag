package handler

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/store"
)

// RestoreStage identifies the last completed or failed restore step.
type RestoreStage string

const (
	RestoreStageValidate  RestoreStage = "validate"
	RestoreStageSnapshot  RestoreStage = "snapshot"
	RestoreStageClose     RestoreStage = "close"
	RestoreStageReplace   RestoreStage = "replace"
	RestoreStageReload    RestoreStage = "reload"
	RestoreStageIntegrity RestoreStage = "integrity"
	RestoreStageRollback  RestoreStage = "rollback"
	RestoreStageComplete  RestoreStage = "complete"
)

// RestoreResult describes a completed or rolled back restore attempt.
type RestoreResult struct {
	Stage        RestoreStage
	SnapshotPath string
	RolledBack   bool
}

// RestoreError preserves the primary error and any rollback failure.
type RestoreError struct {
	Stage    RestoreStage
	Primary  error
	Rollback error
}

func (e *RestoreError) Error() string {
	if e.Rollback != nil {
		return fmt.Sprintf("restore failed at %s: %v; rollback failed: %v", e.Stage, e.Primary, e.Rollback)
	}
	return fmt.Sprintf("restore failed at %s: %v", e.Stage, e.Primary)
}

func (e *RestoreError) Unwrap() []error {
	if e.Rollback != nil {
		return []error{e.Primary, e.Rollback}
	}
	return []error{e.Primary}
}

// RestoreService serializes validation, replacement, reload, and rollback.
type RestoreService struct {
	mu        sync.Mutex
	lifecycle *StoreLifecycle
	dbPath    string
	dims      int
	replace   func(source, destination string) error
	open      func(path string, dims int) (*store.Store, error)
}

func NewRestoreService(lifecycle *StoreLifecycle, dbPath string, dims int) *RestoreService {
	return &RestoreService{
		lifecycle: lifecycle,
		dbPath:    dbPath,
		dims:      dims,
		replace:   replaceDatabase,
		open:      store.New,
	}
}

// Restore installs a prevalidated archive atomically and rolls back on failure.
func (s *RestoreService) Restore(archivePath string) (result RestoreResult, restoreErr error) {
	started := time.Now()
	slog.Info("restore started")
	defer func() {
		observe.RestoreLatency.Observe(time.Since(started).Seconds())
		outcome := "success"
		if restoreErr != nil {
			outcome = string(result.Stage)
		}
		observe.RestoreTotal.WithLabelValues(outcome).Inc()
		slog.Info("restore finished", "stage", result.Stage, "rolled_back", result.RolledBack, "outcome", outcome)
	}()
	s.mu.Lock()
	defer s.mu.Unlock()

	validated, cleanup, err := ValidateAndExtractBackup(archivePath, filepath.Dir(s.dbPath), DefaultBackupValidationLimits)
	if err != nil {
		return RestoreResult{Stage: RestoreStageValidate}, &RestoreError{Stage: RestoreStageValidate, Primary: err}
	}
	defer cleanup()

	s.lifecycle.mu.Lock()
	defer s.lifecycle.mu.Unlock()
	if s.lifecycle.store == nil {
		return RestoreResult{Stage: RestoreStageClose}, &RestoreError{Stage: RestoreStageClose, Primary: fmt.Errorf("store is unavailable")}
	}

	snapshotPath := filepath.Join(filepath.Dir(s.dbPath), fmt.Sprintf(".rag-restore-%d.db", time.Now().UnixNano()))
	if err := s.lifecycle.store.Snapshot(snapshotPath); err != nil {
		return RestoreResult{Stage: RestoreStageSnapshot}, &RestoreError{Stage: RestoreStageSnapshot, Primary: err}
	}
	oldStore := s.lifecycle.store
	if err := oldStore.Close(); err != nil {
		return RestoreResult{Stage: RestoreStageClose, SnapshotPath: snapshotPath}, &RestoreError{Stage: RestoreStageClose, Primary: err}
	}
	s.lifecycle.store = nil

	if err := s.replace(validated.DatabasePath, s.dbPath); err != nil {
		return s.rollback(snapshotPath, RestoreStageReplace, err)
	}
	candidate, err := s.open(s.dbPath, s.dims)
	if err != nil {
		return s.rollback(snapshotPath, RestoreStageReload, err)
	}
	integrity, err := candidate.IntegrityCheck()
	if err != nil || integrity != "ok" {
		_ = candidate.Close()
		if err == nil {
			err = fmt.Errorf("integrity check returned %q", integrity)
		}
		return s.rollback(snapshotPath, RestoreStageIntegrity, err)
	}
	s.lifecycle.store = candidate
	return RestoreResult{Stage: RestoreStageComplete, SnapshotPath: snapshotPath}, nil
}

func (s *RestoreService) rollback(snapshotPath string, stage RestoreStage, primary error) (RestoreResult, error) {
	rollbackErr := s.replace(snapshotPath, s.dbPath)
	if rollbackErr == nil {
		recovered, err := s.open(s.dbPath, s.dims)
		if err != nil {
			rollbackErr = fmt.Errorf("reopen snapshot: %w", err)
		} else if integrity, err := recovered.IntegrityCheck(); err != nil || integrity != "ok" {
			_ = recovered.Close()
			if err != nil {
				rollbackErr = fmt.Errorf("check snapshot integrity: %w", err)
			} else {
				rollbackErr = fmt.Errorf("check snapshot integrity: %q", integrity)
			}
		} else {
			s.lifecycle.store = recovered
		}
	}
	return RestoreResult{Stage: stage, SnapshotPath: snapshotPath, RolledBack: rollbackErr == nil}, &RestoreError{Stage: stage, Primary: primary, Rollback: rollbackErr}
}

func replaceDatabase(source, destination string) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".rag-restore-*")
	if err != nil {
		return fmt.Errorf("create replacement file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	input, err := os.Open(source)
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("open replacement source: %w", err)
	}
	_, copyErr := io.Copy(temporary, input)
	closeInputErr := input.Close()
	closeOutputErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("copy replacement database: %w", copyErr)
	}
	if closeInputErr != nil {
		return fmt.Errorf("close replacement source: %w", closeInputErr)
	}
	if closeOutputErr != nil {
		return fmt.Errorf("close replacement database: %w", closeOutputErr)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return fmt.Errorf("set replacement permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("replace active database: %w", err)
	}
	_ = os.Remove(destination + "-wal")
	_ = os.Remove(destination + "-shm")
	return nil
}

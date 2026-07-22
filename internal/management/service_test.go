package management

import (
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/store"
)

func TestDestructiveOperationsRequireConfirmationBeforeStoreAccess(t *testing.T) {
	service := New(Deps{})
	if _, err := service.Reset(ResetRequest{}); err == nil || err.Error() != "confirm must be true" {
		t.Fatalf("reset error = %v", err)
	}
	if _, err := service.Import(ImportRequest{Path: "/tmp/example.zip"}); err == nil || err.Error() != "confirm must be true" {
		t.Fatalf("import error = %v", err)
	}
	if _, err := service.BackupRestore(ImportRequest{Path: "/tmp/example.zip"}); err == nil || err.Error() != "confirm must be true" {
		t.Fatalf("restore error = %v", err)
	}
	if _, err := service.UpdateSource(nil, UpdateSourceRequest{Source: "source", Content: "content"}); err == nil || err.Error() != "confirm must be true" {
		t.Fatalf("update error = %v", err)
	}
}

func TestRetrievalPatchIsAtomic(t *testing.T) {
	service := New(Deps{})
	valid := true
	if _, err := service.SetRetrievalConfig(RetrievalPatch{RerankEnabled: &valid, ChunkStrategy: stringPtr("not-a-strategy")}); err == nil {
		t.Fatal("expected validation error")
	}
	if got := service.RetrievalConfig(); got.RerankEnabled {
		t.Fatalf("rerank was changed by invalid atomic patch: %#v", got)
	}
}

func TestImportRejectsNonLocalArchivePathBeforeQueueing(t *testing.T) {
	st, err := store.New(t.TempDir()+"/rag.db", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := New(Deps{Config: &config.Config{}, Store: st})
	if _, err := service.Import(ImportRequest{Path: "https://example.invalid/archive.zip", Confirm: true}); err == nil || err.Error() != "archive path must be a local filesystem path" {
		t.Fatalf("import error = %v", err)
	}
}

func stringPtr(value string) *string { return &value }

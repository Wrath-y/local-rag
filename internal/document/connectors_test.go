package document

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionRegistryLoadsTXTJSONAndRejectsUnsafeMetadata(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "guide.txt")
	jsonPath := filepath.Join(dir, "guide.json")
	if err := os.WriteFile(txt, []byte("\xef\xbb\xbfhello\r\nworld"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, []byte(`{"z":1,"a":{"b":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := BuiltinRegistryWithOptions(nil, Options{AllowedLocalPaths: []string{dir}, Limits: Limits{SourceBytes: 1024, Documents: 2}})
	docs, err := registry.Load(context.Background(), Request{Kind: InputTXT, Path: txt})
	if err != nil || len(docs) != 1 || docs[0].Content != "hello\nworld" || docs[0].Metadata.Provenance["loader"] != "txt/v1" {
		t.Fatalf("txt docs=%#v err=%v", docs, err)
	}
	docs, err = registry.Load(context.Background(), Request{Kind: InputJSON, Path: jsonPath})
	if err != nil || !strings.Contains(docs[0].Content, `"a"`) || strings.Index(docs[0].Content, `"a"`) > strings.Index(docs[0].Content, `"z"`) {
		t.Fatalf("json docs=%#v err=%v", docs, err)
	}
	err = ValidateDocuments([]Document{{Content: "x", Metadata: Metadata{Source: "s", DisplayName: "s", Kind: InputTXT, Provenance: map[string]string{"uri": "https://token@example.test"}}}})
	if category, ok := CategoryOf(err); !ok || category != InvalidInput {
		t.Fatalf("unsafe metadata error=%v", err)
	}
}

func TestRequestLimitsOnlyTightenServerLimits(t *testing.T) {
	server := Limits{SourceBytes: 10, Documents: 2, DurationSecs: 5}
	effective := effectiveLimits(server, Limits{SourceBytes: 100, Documents: 8, DurationSecs: 20})
	if effective.SourceBytes != 10 || effective.Documents != 2 || effective.DurationSecs != 5 {
		t.Fatalf("effective=%+v", effective)
	}
	tight := effectiveLimits(server, Limits{SourceBytes: 4, Documents: 1, DurationSecs: 2})
	if tight.SourceBytes != 4 || tight.Documents != 1 || tight.DurationSecs != 2 {
		t.Fatalf("tight=%+v", tight)
	}
}

func TestGitLoaderLocalRevisionAndExclusion(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "test@example.invalid")
	git(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("skip"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "fixture")
	registry := BuiltinRegistryWithOptions(nil, Options{AllowedLocalPaths: []string{dir}, Limits: Limits{Documents: 10, GitFiles: 10, GitFileBytes: 1024, GitTotalBytes: 1024}})
	docs, err := registry.Load(context.Background(), Request{Kind: InputGit, Path: dir, Exclusions: []string{"skip.txt"}})
	if err != nil || len(docs) != 1 || docs[0].Metadata.Provenance["repository_path"] != "keep.txt" || len(docs[0].Metadata.Provenance["resolved_revision"]) != 40 {
		t.Fatalf("docs=%#v err=%v", docs, err)
	}
}

func TestDOCXAndMalformedJSONFailuresAreAtomicAtLoaderBoundary(t *testing.T) {
	dir := t.TempDir()
	docx := filepath.Join(dir, "guide.docx")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(`<w:document><w:body><w:p><w:r><w:t>Hello DOCX</w:t></w:r></w:p></w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docx, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte(`{"bad":`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := BuiltinRegistryWithOptions(nil, Options{AllowedLocalPaths: []string{dir}, Limits: Limits{SourceBytes: 1024, ExtractedBytes: 1024}})
	docs, err := registry.Load(context.Background(), Request{Kind: InputDOCX, Path: docx})
	if err != nil || len(docs) != 1 || docs[0].Content != "Hello DOCX" {
		t.Fatalf("docx docs=%#v err=%v", docs, err)
	}
	_, err = registry.Load(context.Background(), Request{Kind: InputJSON, Path: badJSON})
	if category, ok := CategoryOf(err); !ok || category != ExtractionFailed {
		t.Fatalf("bad json category=%q err=%v", category, err)
	}
}

func TestGitTemporaryCloneIsOwnedAndCleanable(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "test@example.invalid")
	git(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "guide.txt"), []byte("guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "fixture")
	workspace, err := cloneGitWorkspace(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(workspace)
	if _, err := os.Stat(filepath.Join(root, ".local-rag-owned")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("workspace was not removed: %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

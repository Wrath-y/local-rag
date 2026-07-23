package document

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

func effectiveLimits(server, request Limits) Limits {
	return Limits{SourceBytes: lowerLimit(server.SourceBytes, request.SourceBytes), Documents: lowerInt(server.Documents, request.Documents), ExtractedBytes: lowerLimit(server.ExtractedBytes, request.ExtractedBytes), DurationSecs: lowerInt(server.DurationSecs, request.DurationSecs), GitFiles: lowerInt(server.GitFiles, request.GitFiles), GitFileBytes: lowerLimit(server.GitFileBytes, request.GitFileBytes), GitTotalBytes: lowerLimit(server.GitTotalBytes, request.GitTotalBytes)}
}
func lowerLimit(server, requested int64) int64 {
	if server > 0 && (requested == 0 || requested > server) {
		return server
	}
	return requested
}
func lowerInt(server, requested int) int {
	if server > 0 && (requested == 0 || requested > server) {
		return server
	}
	return requested
}
func minPositive(a, b int64) int64 {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func validateLocalPath(options Options, value string) error {
	path, err := filepath.Abs(value)
	if err != nil {
		return NewError(InvalidInput, "Provide a valid local file path.", err)
	}
	if len(options.AllowedLocalPaths) == 0 {
		return nil
	}
	for _, root := range options.AllowedLocalPaths {
		rootPath, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootPath, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return NewError(PolicyRejected, "The local path is outside configured connector directories.", nil)
}
func canonicalLocalPath(value string) string {
	path, err := filepath.Abs(value)
	if err != nil {
		return filepath.Clean(value)
	}
	return filepath.Clean(path)
}
func allowsScheme(options Options, value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if len(options.AllowedURLSchemes) == 0 {
		return parsed.Scheme == "http" || parsed.Scheme == "https"
	}
	for _, allowed := range options.AllowedURLSchemes {
		if strings.EqualFold(parsed.Scheme, allowed) {
			return true
		}
	}
	return false
}
func redactURL(value *url.URL) string {
	if value == nil {
		return ""
	}
	copy := *value
	copy.User = nil
	copy.RawQuery = ""
	copy.ForceQuery = false
	copy.Fragment = ""
	return copy.String()
}
func mergedProvenance(base, extra map[string]string) map[string]string {
	result := normalizeProvenance(base)
	if result == nil {
		result = map[string]string{}
	}
	for key, value := range extra {
		if value != "" {
			result[key] = value
		}
	}
	return result
}

// TXTLoader reads plain text as UTF-8, strips an optional UTF-8 BOM, and
// normalizes CRLF/CR to LF without attempting lossy legacy decoding.
type TXTLoader struct{ Options Options }

func (TXTLoader) Name() string { return "txt" }
func (loader TXTLoader) Supports(request Request) bool {
	return request.Kind == InputTXT || (request.Kind == InputAuto && isTextExtension(request.Path))
}
func (loader TXTLoader) Load(_ context.Context, request Request) ([]Document, error) {
	if err := validateLocalPath(loader.Options, request.Path); err != nil {
		return nil, err
	}
	data, err := readBoundedFile(request.Path, effectiveLimits(loader.Options.Limits, request.Limits).SourceBytes)
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = canonicalLocalPath(request.Path)
	}
	return []Document{{Content: text, Metadata: Metadata{Source: source, DisplayName: filepath.Base(request.Path), Kind: InputTXT, Provenance: mergedProvenance(request.Provenance, map[string]string{"source_uri": canonicalLocalPath(request.Path), "loader": "txt/v1", "source_kind": "txt", "content_type": "text/plain", "encoding": "utf-8"})}}}, nil
}
func isTextExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".markdown", ".rst", ".csv", ".log", ".go", ".py", ".js", ".ts", ".java", ".c", ".h", ".yaml", ".yml", ".toml", ".xml", ".html", ".css", ".sh":
		return path != ""
	default:
		return false
	}
}
func readBoundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, unavailableFileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, NewError(UnavailableInput, "The requested local path is not a readable file.", nil)
	}
	if limit > 0 && info.Size() > limit {
		return nil, NewError(LimitExceeded, "The local file exceeds the configured size limit.", nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, unavailableFileError(err)
	}
	return data, nil
}

// JSONLoader emits a canonical, key-sorted JSON representation. A malformed
// source fails before any document is handed to the ingestion pipeline.
type JSONLoader struct{ Options Options }

func (JSONLoader) Name() string { return "json" }
func (loader JSONLoader) Supports(request Request) bool {
	return request.Kind == InputJSON || (request.Kind == InputAuto && strings.EqualFold(filepath.Ext(request.Path), ".json"))
}
func (loader JSONLoader) Load(_ context.Context, request Request) ([]Document, error) {
	if err := validateLocalPath(loader.Options, request.Path); err != nil {
		return nil, err
	}
	data, err := readBoundedFile(request.Path, effectiveLimits(loader.Options.Limits, request.Limits).SourceBytes)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, NewError(ExtractionFailed, "The JSON document is malformed.", err)
	}
	if decoder.More() {
		return nil, NewError(ExtractionFailed, "The JSON document is malformed.", nil)
	}
	canonical := renderJSON(value)
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = canonicalLocalPath(request.Path)
	}
	return []Document{{Content: canonical, Metadata: Metadata{Source: source, DisplayName: filepath.Base(request.Path), Kind: InputJSON, Provenance: mergedProvenance(request.Provenance, map[string]string{"source_uri": canonicalLocalPath(request.Path), "loader": "json/v1", "source_kind": "json", "content_type": "application/json", "json_path": "$"})}}}, nil
}
func renderJSON(value any) string {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%q: %s", k, renderJSON(v[k])))
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n}"
	case []any:
		parts := make([]string, len(v))
		for i := range v {
			parts[i] = renderJSON(v[i])
		}
		return "[\n" + strings.Join(parts, ",\n") + "\n]"
	case string:
		return fmt.Sprintf("%q", v)
	default:
		return fmt.Sprint(v)
	}
}

// DOCXLoader extracts textual nodes from document.xml without evaluating any
// embedded content or macros.
type DOCXLoader struct{ Options Options }

func (DOCXLoader) Name() string { return "docx" }
func (loader DOCXLoader) Supports(request Request) bool {
	return request.Kind == InputDOCX || (request.Kind == InputAuto && strings.EqualFold(filepath.Ext(request.Path), ".docx"))
}
func (loader DOCXLoader) Load(_ context.Context, request Request) ([]Document, error) {
	if err := validateLocalPath(loader.Options, request.Path); err != nil {
		return nil, err
	}
	data, err := readBoundedFile(request.Path, effectiveLimits(loader.Options.Limits, request.Limits).SourceBytes)
	if err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, NewError(ExtractionFailed, "The DOCX document could not be read.", err)
	}
	var documentXML *zip.File
	for _, f := range archive.File {
		if f.Name == "word/document.xml" {
			documentXML = f
			break
		}
	}
	if documentXML == nil {
		return nil, NewError(ExtractionFailed, "The DOCX document does not contain usable text.", nil)
	}
	reader, err := documentXML.Open()
	if err != nil {
		return nil, NewError(ExtractionFailed, "The DOCX document could not be read.", err)
	}
	defer reader.Close()
	limit := effectiveLimits(loader.Options.Limits, request.Limits).ExtractedBytes
	if limit <= 0 {
		limit = 50 << 20
	}
	tokenizer := html.NewTokenizer(io.LimitReader(reader, limit+1))
	var text []string
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt == html.TextToken {
			text = append(text, string(tokenizer.Text()))
		}
	}
	content := strings.Join(strings.Fields(strings.Join(text, " ")), " ")
	if content == "" {
		return nil, NewError(ExtractionFailed, "The DOCX document does not contain usable text.", nil)
	}
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = canonicalLocalPath(request.Path)
	}
	return []Document{{Content: content, Metadata: Metadata{Source: source, DisplayName: filepath.Base(request.Path), Kind: InputDOCX, Provenance: mergedProvenance(request.Provenance, map[string]string{"source_uri": canonicalLocalPath(request.Path), "loader": "docx/v1", "source_kind": "docx", "content_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"})}}}, nil
}

// GitLoader uses argument-vector git execution and a managed temporary clone
// directory. It never accepts credential-bearing remotes or mutates a source.
type GitLoader struct{ Options Options }

// CleanupStaleGitWorkspaces removes only temp directories bearing this
// connector's ownership marker. It is safe to run at process startup.
func CleanupStaleGitWorkspaces() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "local-rag-git-") {
			continue
		}
		path := filepath.Join(os.TempDir(), entry.Name())
		if _, err := os.Stat(filepath.Join(path, ".local-rag-owned")); err == nil {
			_ = os.RemoveAll(path)
		}
	}
}

func (GitLoader) Name() string                         { return "git" }
func (loader GitLoader) Supports(request Request) bool { return request.Kind == InputGit }
func (loader GitLoader) Load(ctx context.Context, request Request) ([]Document, error) {
	repo, remote, err := loader.gitWorkspace(ctx, request)
	if err != nil {
		return nil, err
	}
	if remote {
		defer os.RemoveAll(filepath.Dir(repo))
	}
	identity := gitIdentity(ctx, repo, request, remote)
	ref := strings.TrimSpace(request.Ref)
	revision, err := gitOutput(ctx, repo, "rev-parse", refOrHEAD(ref)+"^{commit}")
	if err != nil {
		return nil, NewError(UnavailableInput, "The requested Git revision could not be resolved.", err)
	}
	if remote {
		if _, err := gitOutput(ctx, repo, "checkout", "--detach", revision); err != nil {
			return nil, NewError(UnavailableInput, "The requested Git revision could not be selected.", err)
		}
	} else if ref != "" {
		current, currentErr := gitOutput(ctx, repo, "rev-parse", "HEAD")
		if currentErr != nil || strings.TrimSpace(current) != strings.TrimSpace(revision) {
			// Never checkout in the caller's worktree. A temporary local clone
			// provides deterministic traversal of the requested branch/tag/SHA.
			selected, cloneErr := cloneGitWorkspace(ctx, repo)
			if cloneErr != nil {
				return nil, cloneErr
			}
			defer os.RemoveAll(filepath.Dir(selected))
			repo = selected
			if _, err := gitOutput(ctx, repo, "checkout", "--detach", revision); err != nil {
				return nil, NewError(UnavailableInput, "The requested Git revision could not be selected.", err)
			}
		}
	}
	return loader.walkGit(ctx, repo, identity, ref, strings.TrimSpace(revision), request)
}
func refOrHEAD(ref string) string {
	if ref == "" {
		return "HEAD"
	}
	return ref
}
func (loader GitLoader) gitWorkspace(ctx context.Context, request Request) (string, bool, error) {
	if strings.TrimSpace(request.Path) != "" {
		if err := validateLocalPath(loader.Options, request.Path); err != nil {
			return "", false, err
		}
		if _, err := gitOutput(ctx, request.Path, "rev-parse", "--git-dir"); err != nil {
			return "", false, NewError(InvalidInput, "The local path is not a Git repository.", err)
		}
		return canonicalLocalPath(request.Path), false, nil
	}
	parsed, err := url.Parse(strings.TrimSpace(request.URL))
	if err != nil || parsed.User != nil || parsed.Host == "" || !(parsed.Scheme == "https" || parsed.Scheme == "ssh") {
		return "", false, NewError(InvalidInput, "Provide a credential-free HTTPS or SSH Git repository URL.", err)
	}
	dir, err := os.MkdirTemp("", "local-rag-git-")
	if err != nil {
		return "", false, NewError(IngestFailed, "A temporary Git workspace could not be created.", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".local-rag-owned"), []byte("v1\n"), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", false, NewError(IngestFailed, "A temporary Git workspace could not be created.", err)
	}
	clonePath := filepath.Join(dir, "repository")
	if _, err := gitCommand(ctx, "clone", "--no-recurse-submodules", redactURL(parsed), clonePath); err != nil {
		os.RemoveAll(dir)
		return "", false, NewError(UnavailableInput, "The Git repository could not be cloned.", err)
	}
	return clonePath, true, nil
}
func cloneGitWorkspace(ctx context.Context, source string) (string, error) {
	dir, err := os.MkdirTemp("", "local-rag-git-")
	if err != nil {
		return "", NewError(IngestFailed, "A temporary Git workspace could not be created.", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".local-rag-owned"), []byte("v1\n"), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", NewError(IngestFailed, "A temporary Git workspace could not be created.", err)
	}
	clonePath := filepath.Join(dir, "repository")
	if _, err := gitCommand(ctx, "clone", "--no-local", "--no-recurse-submodules", source, clonePath); err != nil {
		os.RemoveAll(dir)
		return "", NewError(UnavailableInput, "The local Git repository could not be prepared.", err)
	}
	return clonePath, nil
}
func gitCommand(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git command failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	all := append([]string{"-C", dir}, args...)
	return gitCommand(ctx, all...)
}
func gitIdentity(ctx context.Context, repo string, request Request, remote bool) string {
	if remote {
		parsed, _ := url.Parse(request.URL)
		return redactURL(parsed)
	}
	out, err := gitOutput(ctx, repo, "config", "--get", "remote.origin.url")
	if err == nil && out != "" {
		parsed, parseErr := url.Parse(out)
		if parseErr == nil {
			return redactURL(parsed)
		}
	}
	return canonicalLocalPath(repo)
}
func (loader GitLoader) walkGit(ctx context.Context, repo, identity, ref, revision string, request Request) ([]Document, error) {
	limits := effectiveLimits(loader.Options.Limits, request.Limits)
	if limits.GitFiles == 0 {
		limits.GitFiles = 2000
	}
	if limits.GitFileBytes == 0 {
		limits.GitFileBytes = 2 << 20
	}
	if limits.GitTotalBytes == 0 {
		limits.GitTotalBytes = 50 << 20
	}
	ignore := readGitIgnore(repo)
	for _, pattern := range append(loader.Options.Exclusions, request.Exclusions...) {
		ignore = append(ignore, ignoreRule{pattern: strings.TrimPrefix(pattern, "/")})
	}
	var docs []Document
	var candidates int
	var total int64
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "." {
			return nil
		}
		if ignoredGitPath(rel, ignore) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		candidates++
		if candidates > limits.GitFiles {
			return NewError(LimitExceeded, "The repository exceeds the configured file limit.", nil)
		}
		if !supportedGitFile(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > limits.GitFileBytes {
			return nil
		}
		total += info.Size()
		if total > limits.GitTotalBytes {
			return NewError(LimitExceeded, "The repository exceeds the configured byte limit.", nil)
		}
		doc, err := loader.loadGitFile(ctx, path, rel, identity, ref, revision, request)
		if err != nil {
			return err
		}
		if doc.Content != "" {
			docs = append(docs, doc)
		}
		if len(docs) > limits.Documents {
			return NewError(LimitExceeded, "The repository exceeds the configured document limit.", nil)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, NewError(ExtractionFailed, "The repository contains no supported usable documents.", nil)
	}
	return docs, nil
}
func (loader GitLoader) loadGitFile(ctx context.Context, path, rel, identity, ref, revision string, request Request) (Document, error) {
	kind := InputTXT
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		kind = InputJSON
	case ".pdf":
		kind = InputPDF
	case ".docx":
		kind = InputDOCX
	}
	nested := Request{Kind: kind, Path: path, Limits: request.Limits}
	nestedOptions := loader.Options
	nestedOptions.AllowedLocalPaths = nil // the selected worktree was already policy-validated
	var loaderImpl DocumentLoader
	switch kind {
	case InputJSON:
		loaderImpl = JSONLoader{Options: nestedOptions}
	case InputPDF:
		loaderImpl = PDFLoader{Options: nestedOptions}
	case InputDOCX:
		loaderImpl = DOCXLoader{Options: nestedOptions}
	default:
		loaderImpl = TXTLoader{Options: nestedOptions}
	}
	docs, err := loaderImpl.Load(ctx, nested)
	if err != nil {
		return Document{}, err
	}
	doc := docs[0]
	doc.Metadata.Source = identity + "#" + rel
	doc.Metadata.DisplayName = rel
	doc.Metadata.Kind = InputGit
	doc.Metadata.Provenance = mergedProvenance(doc.Metadata.Provenance, map[string]string{"source_uri": identity, "loader": "git/" + loaderImpl.Name() + "/v1", "source_kind": "git", "repository": identity, "repository_path": rel, "requested_ref": ref, "resolved_revision": revision, "location": rel + "@" + shortRevision(revision)})
	return doc, nil
}
func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
func supportedGitFile(path string) bool {
	return isTextExtension(path) || strings.EqualFold(filepath.Ext(path), ".json") || strings.EqualFold(filepath.Ext(path), ".pdf") || strings.EqualFold(filepath.Ext(path), ".docx")
}

type ignoreRule struct {
	base, pattern string
	negate        bool
}

// readGitIgnore collects root and nested rules in traversal order. It covers
// the common Git patterns (including `!` re-inclusion) without treating a
// caller exclusion as an override of a repository rule.
func readGitIgnore(repo string) []ignoreRule {
	var rules []ignoreRule
	_ = filepath.WalkDir(repo, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != ".gitignore" || strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		base, _ := filepath.Rel(repo, filepath.Dir(path))
		base = filepath.ToSlash(base)
		if base == "." {
			base = ""
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rule := ignoreRule{base: base, negate: strings.HasPrefix(line, "!")}
			rule.pattern = strings.TrimPrefix(strings.TrimPrefix(line, "!"), "/")
			if rule.pattern != "" {
				rules = append(rules, rule)
			}
		}
		return nil
	})
	return rules
}
func ignoredGitPath(path string, rules []ignoreRule) bool {
	ignored := false
	for _, rule := range rules {
		rel := path
		if rule.base != "" {
			if rel != rule.base && !strings.HasPrefix(rel, rule.base+"/") {
				continue
			}
			rel = strings.TrimPrefix(strings.TrimPrefix(rel, rule.base), "/")
		}
		pattern := strings.TrimSuffix(rule.pattern, "/")
		matched, _ := filepath.Match(pattern, rel)
		if !matched && !strings.Contains(pattern, "/") {
			for _, segment := range strings.Split(rel, "/") {
				if ok, _ := filepath.Match(pattern, segment); ok {
					matched = true
					break
				}
			}
		}
		if !matched && strings.HasSuffix(rule.pattern, "/") {
			matched = strings.HasPrefix(rel, pattern+"/") || rel == pattern
		}
		if matched {
			ignored = !rule.negate
		}
	}
	return ignored
}

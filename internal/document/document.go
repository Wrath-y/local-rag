// Package document defines the input-resolution boundary for ingestion.
package document

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxProvenanceEntries = 16
	maxProvenanceLength  = 256
)

// InputKind identifies the supported kind of an ingest request.
type InputKind string

const (
	InputAuto           InputKind = ""
	InputText           InputKind = "text"
	InputLocalFile      InputKind = "local_file"
	InputFeishuDocument InputKind = "feishu_document"
	InputPDF            InputKind = "pdf"
	InputWebURL         InputKind = "web_url"
)

// Request is the normalized input passed to a document loader. Source is an
// optional caller-selected identity override.
type Request struct {
	Kind       InputKind
	Text       string
	Path       string
	URL        string
	Source     string
	Provenance map[string]string
}

// Metadata describes an in-memory document at the ingest boundary.
type Metadata struct {
	Source      string
	DisplayName string
	Kind        InputKind
	Provenance  map[string]string
}

// Document is the normalized result produced by a loader.
type Document struct {
	Content  string
	Metadata Metadata
}

// Result is the aggregate outcome of loading and ingesting a request.
type Result struct {
	Documents   int
	ChunksAdded int
}

// ErrorCategory is safe for callers to use when choosing a transport response.
type ErrorCategory string

const (
	UnsupportedInput ErrorCategory = "unsupported_input"
	InvalidInput     ErrorCategory = "invalid_input"
	UnavailableInput ErrorCategory = "unavailable_input"
	LoadFailed       ErrorCategory = "load_failed"
	IngestFailed     ErrorCategory = "ingest_failed"
)

// Error has a stable category and safe public message while retaining Cause
// for logs and diagnostics.
type Error struct {
	Category ErrorCategory
	Message  string
	Cause    error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// NewError creates a classified error. Message must be suitable for clients;
// implementation errors belong in cause only.
func NewError(category ErrorCategory, message string, cause error) *Error {
	return &Error{Category: category, Message: message, Cause: cause}
}

// CategoryOf returns a classified category, if present in err's chain.
func CategoryOf(err error) (ErrorCategory, bool) {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Category, true
	}
	return "", false
}

// PublicMessage is the safe, actionable message for a classified error.
func PublicMessage(err error) string {
	switch category, ok := CategoryOf(err); {
	case !ok:
		return "Ingestion failed. Please try again later."
	case category == UnsupportedInput:
		return "Provide direct text, a local file path, or a supported Feishu document link."
	case category == InvalidInput:
		return "Provide non-empty content and a valid source identifier."
	case category == UnavailableInput:
		return "The requested input is unavailable. Check that it exists and that you have access."
	case category == LoadFailed:
		return "The input could not be loaded. Check the input and try again."
	default:
		return "Ingestion failed. Please try again later."
	}
}

// DocumentLoader resolves one supported request into normalized documents.
type DocumentLoader interface {
	Name() string
	Supports(Request) bool
	Load(context.Context, Request) ([]Document, error)
}

// Registry chooses the first matching loader in configured order.
type Registry struct{ loaders []DocumentLoader }

func NewRegistry(loaders ...DocumentLoader) *Registry {
	return &Registry{loaders: append([]DocumentLoader(nil), loaders...)}
}

func (r *Registry) Load(ctx context.Context, request Request) ([]Document, error) {
	for _, loader := range r.loaders {
		if loader != nil && loader.Supports(request) {
			return loader.Load(ctx, request)
		}
	}
	return nil, NewError(UnsupportedInput, "This input type is not supported.", nil)
}

// Loaders returns a copy so callers can inspect the configured built-ins
// without changing their deterministic order.
func (r *Registry) Loaders() []DocumentLoader {
	return append([]DocumentLoader(nil), r.loaders...)
}

// ValidateDocuments rejects every invalid document before a pipeline receives
// any document from the loader result.
func ValidateDocuments(documents []Document) error {
	if len(documents) == 0 {
		return NewError(InvalidInput, "The input did not contain any documents.", nil)
	}
	sources := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		if strings.TrimSpace(document.Content) == "" {
			return NewError(InvalidInput, "Document content cannot be blank.", nil)
		}
		metadata := document.Metadata
		if strings.TrimSpace(metadata.Source) == "" || strings.TrimSpace(metadata.DisplayName) == "" || strings.TrimSpace(string(metadata.Kind)) == "" {
			return NewError(InvalidInput, "Document metadata is incomplete.", nil)
		}
		if _, exists := sources[metadata.Source]; exists {
			return NewError(InvalidInput, "Document sources must be unique within one request.", nil)
		}
		sources[metadata.Source] = struct{}{}
		if err := validateProvenance(metadata.Provenance); err != nil {
			return err
		}
	}
	return nil
}

func normalizeProvenance(provenance map[string]string) map[string]string {
	if len(provenance) == 0 {
		return nil
	}
	copy := make(map[string]string, len(provenance))
	for key, value := range provenance {
		copy[key] = value
	}
	return copy
}

func validateProvenance(provenance map[string]string) error {
	if len(provenance) > maxProvenanceEntries {
		return NewError(InvalidInput, "Document provenance contains too many attributes.", nil)
	}
	for key, value := range provenance {
		if strings.TrimSpace(key) == "" || len(key) > maxProvenanceLength || len(value) > maxProvenanceLength {
			return NewError(InvalidInput, "Document provenance contains an invalid attribute.", nil)
		}
	}
	return nil
}

// Pipeline receives documents only after the complete loader result validates.
type Pipeline interface {
	IngestDocument(context.Context, Document) (int, error)
}

type PipelineFunc func(context.Context, Document) (int, error)

func (f PipelineFunc) IngestDocument(ctx context.Context, document Document) (int, error) {
	return f(ctx, document)
}

// Service coordinates loader selection, complete-result validation, and the
// existing ingestion pipeline.
type Service struct {
	Registry *Registry
	Pipeline Pipeline
}

func (s Service) Ingest(ctx context.Context, request Request) (Result, error) {
	if s.Registry == nil || s.Pipeline == nil {
		return Result{}, NewError(IngestFailed, "Ingestion is not configured.", nil)
	}
	documents, err := s.Registry.Load(ctx, request)
	if err != nil {
		return Result{}, classifyLoadError(err)
	}
	if err := ValidateDocuments(documents); err != nil {
		return Result{}, err
	}
	result := Result{Documents: len(documents)}
	for _, document := range documents {
		added, err := s.Pipeline.IngestDocument(ctx, document)
		if err != nil {
			if _, ok := CategoryOf(err); ok {
				return Result{}, err
			}
			return Result{}, NewError(IngestFailed, "The document could not be ingested. Please try again later.", err)
		}
		result.ChunksAdded += added
	}
	return result, nil
}

func classifyLoadError(err error) error {
	if _, ok := CategoryOf(err); ok {
		return err
	}
	return NewError(LoadFailed, "The input could not be loaded. Check the input and try again.", err)
}

// DirectTextLoader accepts literal content.
type DirectTextLoader struct{}

func (DirectTextLoader) Name() string { return "direct-text" }
func (DirectTextLoader) Supports(request Request) bool {
	return request.Kind == InputText || (request.Kind == InputAuto && request.Text != "")
}
func (DirectTextLoader) Load(_ context.Context, request Request) ([]Document, error) {
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = "manual"
	}
	return []Document{{Content: request.Text, Metadata: Metadata{
		Source: source, DisplayName: source, Kind: InputText, Provenance: normalizeProvenance(request.Provenance),
	}}}, nil
}

// LocalFileLoader reads only an explicitly named local file.
type LocalFileLoader struct{}

func (LocalFileLoader) Name() string { return "local-file" }
func (LocalFileLoader) Supports(request Request) bool {
	return request.Kind == InputLocalFile || (request.Kind == InputAuto && request.Path != "")
}
func (LocalFileLoader) Load(_ context.Context, request Request) ([]Document, error) {
	if unsupportedLocalFile(request.Path) {
		return nil, NewError(UnsupportedInput, "This local file format is not supported. Provide a plain-text file.", nil)
	}
	info, err := os.Stat(request.Path)
	if err != nil {
		return nil, unavailableFileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, NewError(UnavailableInput, "The requested local path is not a readable file.", nil)
	}
	content, err := os.ReadFile(request.Path)
	if err != nil {
		return nil, unavailableFileError(err)
	}
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = filepath.Base(request.Path)
	}
	return []Document{{Content: string(content), Metadata: Metadata{
		Source: source, DisplayName: source, Kind: InputLocalFile, Provenance: normalizeProvenance(request.Provenance),
	}}}, nil
}

func unsupportedLocalFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx", ".odt", ".ods", ".odp", ".zip", ".tar", ".gz", ".tgz", ".rar", ".7z":
		return true
	default:
		return false
	}
}

func unavailableFileError(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return NewError(UnavailableInput, "The requested local file is unavailable.", err)
	}
	return NewError(LoadFailed, "The requested local file could not be read.", err)
}

// FeishuResolver resolves the content behind an existing Feishu integration.
// It intentionally has no default network implementation.
type FeishuResolver interface {
	ResolveFeishuDocument(context.Context, string) (string, error)
}

// FeishuLoader adapts the existing Feishu content-resolution boundary.
type FeishuLoader struct{ Resolver FeishuResolver }

func (FeishuLoader) Name() string { return "feishu-document" }
func (FeishuLoader) Supports(request Request) bool {
	return request.Kind == InputFeishuDocument || (request.Kind == InputAuto && isFeishuURL(request.URL))
}
func (loader FeishuLoader) Load(ctx context.Context, request Request) ([]Document, error) {
	if !isFeishuURL(request.URL) {
		return nil, NewError(InvalidInput, "Provide a supported Feishu or LarkSuite document link.", nil)
	}
	if loader.Resolver == nil {
		return nil, NewError(UnavailableInput, "Feishu document resolution is not available in this service.", nil)
	}
	content, err := loader.Resolver.ResolveFeishuDocument(ctx, request.URL)
	if err != nil {
		return nil, classifyFeishuError(err)
	}
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = request.URL
	}
	return []Document{{Content: content, Metadata: Metadata{
		Source: source, DisplayName: source, Kind: InputFeishuDocument, Provenance: normalizeProvenance(request.Provenance),
	}}}, nil
}

func classifyFeishuError(err error) error {
	if _, ok := CategoryOf(err); ok {
		return err
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return NewError(UnavailableInput, "The requested Feishu document is unavailable.", err)
	}
	return NewError(LoadFailed, "The Feishu document could not be loaded. Check access and try again.", err)
}

func isFeishuURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	withoutScheme := strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
	host := strings.Split(strings.Split(withoutScheme, "/")[0], ":")[0]
	return host == "feishu.cn" || strings.HasSuffix(host, ".feishu.cn") || host == "larksuite.com" || strings.HasSuffix(host, ".larksuite.com")
}

// BuiltinRegistry exposes exactly the currently supported adapters in their
// documented precedence order.
func BuiltinRegistry(resolver FeishuResolver) *Registry {
	return NewRegistry(FeishuLoader{Resolver: resolver}, WebLoader{}, PDFLoader{}, LocalFileLoader{}, DirectTextLoader{})
}

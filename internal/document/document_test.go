package document

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testLoader struct {
	name      string
	supports  bool
	documents []Document
	err       error
	calls     int
}

func (loader *testLoader) Name() string          { return loader.name }
func (loader *testLoader) Supports(Request) bool { return loader.supports }
func (loader *testLoader) Load(context.Context, Request) ([]Document, error) {
	loader.calls++
	return loader.documents, loader.err
}

type recordingPipeline struct {
	documents []Document
	err       error
}

func (pipeline *recordingPipeline) IngestDocument(_ context.Context, document Document) (int, error) {
	pipeline.documents = append(pipeline.documents, document)
	if pipeline.err != nil {
		return 0, pipeline.err
	}
	return 2, nil
}

func validDocument(source string) Document {
	return Document{Content: "content", Metadata: Metadata{Source: source, DisplayName: source, Kind: InputText}}
}

func TestRegistryUsesFirstMatchingLoader(t *testing.T) {
	first := &testLoader{name: "first", supports: true, documents: []Document{validDocument("first")}}
	second := &testLoader{name: "second", supports: true, documents: []Document{validDocument("second")}}
	documents, err := NewRegistry(first, second).Load(context.Background(), Request{})
	if err != nil || documents[0].Metadata.Source != "first" {
		t.Fatalf("registry result = %#v, %v", documents, err)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("loader calls = first:%d second:%d", first.calls, second.calls)
	}
}

func TestRegistryRejectsUnsupportedInput(t *testing.T) {
	_, err := NewRegistry().Load(context.Background(), Request{URL: "https://example.com"})
	if category, ok := CategoryOf(err); !ok || category != UnsupportedInput {
		t.Fatalf("category = %q, %v", category, err)
	}
}

func TestServiceValidatesWholeResultBeforePipeline(t *testing.T) {
	loader := &testLoader{supports: true, documents: []Document{validDocument("one"), validDocument("one")}}
	pipeline := &recordingPipeline{}
	_, err := (Service{Registry: NewRegistry(loader), Pipeline: pipeline}).Ingest(context.Background(), Request{})
	if category, ok := CategoryOf(err); !ok || category != InvalidInput {
		t.Fatalf("category = %q, %v", category, err)
	}
	if len(pipeline.documents) != 0 {
		t.Fatalf("pipeline received %d documents before validation", len(pipeline.documents))
	}
}

func TestDirectTextRejectsBlankContentBeforePipeline(t *testing.T) {
	pipeline := &recordingPipeline{}
	_, err := (Service{Registry: BuiltinRegistry(nil), Pipeline: pipeline}).Ingest(context.Background(), Request{Kind: InputText, Text: " \n\t "})
	if category, ok := CategoryOf(err); !ok || category != InvalidInput {
		t.Fatalf("category = %q, %v", category, err)
	}
	if len(pipeline.documents) != 0 {
		t.Fatalf("pipeline received blank direct text")
	}
}

func TestServiceClassifiesPipelineFailureWithoutLeakingCause(t *testing.T) {
	loader := &testLoader{supports: true, documents: []Document{validDocument("one")}}
	pipeline := &recordingPipeline{err: errors.New("database password: secret")}
	_, err := (Service{Registry: NewRegistry(loader), Pipeline: pipeline}).Ingest(context.Background(), Request{})
	if category, ok := CategoryOf(err); !ok || category != IngestFailed {
		t.Fatalf("category = %q, %v", category, err)
	}
	if message := PublicMessage(err); message == "" || message == err.Error() {
		t.Fatalf("unsafe public message %q", message)
	}
}

func TestBuiltinAdaptersNormalizeSupportedInputs(t *testing.T) {
	temporary := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(temporary, []byte("local content"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := BuiltinRegistry(testFeishuResolver{content: "feishu content"})
	tests := []struct {
		name    string
		request Request
		source  string
		kind    InputKind
	}{
		{"text default", Request{Kind: InputText, Text: "hello"}, "manual", InputText},
		{"text override", Request{Kind: InputText, Text: "hello", Source: "chosen"}, "chosen", InputText},
		{"local default", Request{Kind: InputLocalFile, Path: temporary}, "notes.txt", InputLocalFile},
		{"local override", Request{Kind: InputLocalFile, Path: temporary, Source: "chosen"}, "chosen", InputLocalFile},
		{"feishu default", Request{Kind: InputFeishuDocument, URL: "https://example.feishu.cn/docx/token"}, "https://example.feishu.cn/docx/token", InputFeishuDocument},
		{"feishu override", Request{Kind: InputFeishuDocument, URL: "https://example.larksuite.com/docx/token", Source: "chosen"}, "chosen", InputFeishuDocument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			documents, err := registry.Load(context.Background(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			if got := documents[0].Metadata; got.Source != test.source || got.DisplayName != test.source || got.Kind != test.kind {
				t.Fatalf("metadata = %#v", got)
			}
		})
	}
}

func TestBuiltinAdaptersClassifyUnavailableAndResolverFailures(t *testing.T) {
	_, err := (LocalFileLoader{}).Load(context.Background(), Request{Path: filepath.Join(t.TempDir(), "missing")})
	if category, ok := CategoryOf(err); !ok || category != UnavailableInput {
		t.Fatalf("missing file category = %q, %v", category, err)
	}
	_, err = (FeishuLoader{Resolver: testFeishuResolver{err: errors.New("credentials=secret")}}).Load(context.Background(), Request{URL: "https://example.feishu.cn/docx/token"})
	if category, ok := CategoryOf(err); !ok || category != LoadFailed {
		t.Fatalf("resolver category = %q, %v", category, err)
	}
	if message := PublicMessage(err); message == "" || message == err.Error() {
		t.Fatalf("unsafe resolver message %q", message)
	}
}

func TestBuiltinRegistryRejectsUnsupportedURLScheme(t *testing.T) {
	_, err := BuiltinRegistry(nil).Load(context.Background(), Request{URL: "ftp://example.com/document"})
	if category, ok := CategoryOf(err); !ok || category != UnsupportedInput {
		t.Fatalf("category = %q, %v", category, err)
	}
}

func TestBuiltinRegistryContainsOnlySupportedLoaders(t *testing.T) {
	loaders := BuiltinRegistry(nil).Loaders()
	if len(loaders) != 5 || loaders[0].Name() != "feishu-document" || loaders[1].Name() != "web-url" || loaders[2].Name() != "pdf" || loaders[3].Name() != "local-file" || loaders[4].Name() != "direct-text" {
		t.Fatalf("built-in loaders = %#v", loaders)
	}
}

type testFeishuResolver struct {
	content string
	err     error
}

func (resolver testFeishuResolver) ResolveFeishuDocument(context.Context, string) (string, error) {
	return resolver.content, resolver.err
}

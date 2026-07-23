package document

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFLoaderExtractsTextAndDerivesSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guide.pdf")
	if err := os.WriteFile(path, minimalPDF("PDF hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	documents, err := (PDFLoader{}).Load(context.Background(), Request{Kind: InputPDF, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || !strings.Contains(documents[0].Content, "PDF hello") || documents[0].Metadata.Source != "guide.pdf" || documents[0].Metadata.Kind != InputPDF {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestPDFLoaderRejectsCorruptAndMissingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pdf")
	if err := os.WriteFile(path, []byte("not a PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{path, filepath.Join(t.TempDir(), "missing.pdf")} {
		_, err := (PDFLoader{}).Load(context.Background(), Request{Kind: InputPDF, Path: path})
		if category, ok := CategoryOf(err); !ok || (category != LoadFailed && category != UnavailableInput) {
			t.Fatalf("path %q category = %q, %v", path, category, err)
		}
	}
}

func TestWebValidationAndExtraction(t *testing.T) {
	for _, value := range []string{"ftp://example.com/file", "http://user:password@example.com", "http://127.0.0.1/private"} {
		_, err := parseSafeWebURL(context.Background(), nil, value)
		if err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
	content, err := extractWebContent("text/html; charset=utf-8", []byte(`<html><head><script>secret()</script><style>.x{}</style></head><body><nav>menu</nav><article>Hello <b>world</b></article><footer>footer</footer></body></html>`))
	if err != nil || content != "Hello world" {
		t.Fatalf("content = %q, %v", content, err)
	}
	content, err = extractWebContent("text/plain", []byte(" one\n\ttwo "))
	if err != nil || content != "one two" {
		t.Fatalf("plain text = %q, %v", content, err)
	}
}

func TestWebResponseRejectsUnsupportedContent(t *testing.T) {
	_, err := extractWebContent("application/pdf", []byte("binary"))
	if category, ok := CategoryOf(err); !ok || category != LoadFailed {
		t.Fatalf("category = %q, %v", category, err)
	}
}

func TestWebLoaderNormalizesOnePublicHTMLResource(t *testing.T) {
	loader := WebLoader{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<main>Public page</main><script>ignored()</script>")),
			Request:    request,
		}, nil
	})}}
	documents, err := loader.Load(context.Background(), Request{Kind: InputWebURL, URL: "https://93.184.216.34/article"})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Content != "Public page" || documents[0].Metadata.Source != "https://93.184.216.34/article" || documents[0].Metadata.Kind != InputWebURL {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestWebLoaderEnforcesResponseAndRedirectLimits(t *testing.T) {
	loader := WebLoader{Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxWebResponseBytes+1)))),
			Request:    request,
		}, nil
	})}}
	_, err := loader.Load(context.Background(), Request{Kind: InputWebURL, URL: "https://93.184.216.34/large"})
	if category, ok := CategoryOf(err); !ok || category != LoadFailed {
		t.Fatalf("oversize category = %q, %v", category, err)
	}
	client := newSafeWebClient(net.DefaultResolver)
	publicURL, err := url.Parse("https://93.184.216.34/redirect")
	if err != nil {
		t.Fatal(err)
	}
	request := &http.Request{URL: publicURL}
	if err := client.CheckRedirect(request, make([]*http.Request, maxWebRedirects)); err != nil {
		t.Fatalf("third redirect should be permitted: %v", err)
	}
	if err := client.CheckRedirect(request, make([]*http.Request, maxWebRedirects+1)); err == nil {
		t.Fatal("expected fourth redirect to be rejected")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func minimalPDF(text string) []byte {
	stream := "BT\n/F1 16 Tf\n72 720 Td\n(" + text + ") Tj\nET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 4 0 R >> >> /MediaBox [0 0 612 792] /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}
	var builder strings.Builder
	builder.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = builder.Len()
		fmt.Fprintf(&builder, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := builder.Len()
	fmt.Fprintf(&builder, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&builder, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&builder, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(builder.String())
}

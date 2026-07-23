package document

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
	"golang.org/x/net/html"
)

const (
	maxPDFBytes         int64 = 20 << 20
	maxWebResponseBytes int64 = 10 << 20
	maxWebRedirects           = 3
	webRequestTimeout         = 15 * time.Second
)

// PDFLoader extracts embedded text from an explicitly selected local PDF.
type PDFLoader struct{ Options Options }

func (PDFLoader) Name() string { return "pdf" }
func (PDFLoader) Supports(request Request) bool {
	return request.Kind == InputPDF || (request.Kind == InputAuto && strings.EqualFold(filepath.Ext(request.Path), ".pdf"))
}
func (loader PDFLoader) Load(_ context.Context, request Request) ([]Document, error) {
	if err := validateLocalPath(loader.Options, request.Path); err != nil {
		return nil, err
	}
	info, err := os.Stat(request.Path)
	if err != nil {
		return nil, unavailableFileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, NewError(UnavailableInput, "The requested PDF path is not a readable file.", nil)
	}
	limit := effectiveLimits(loader.Options.Limits, request.Limits).SourceBytes
	if info.Size() > maxPDFBytes || (limit > 0 && info.Size() > limit) {
		return nil, NewError(LoadFailed, "The PDF is too large to ingest.", nil)
	}
	file, reader, err := pdf.Open(request.Path)
	if err != nil {
		return nil, NewError(LoadFailed, "The PDF could not be read.", err)
	}
	defer file.Close()
	plainText, err := reader.GetPlainText()
	if err != nil {
		return nil, NewError(LoadFailed, "Text could not be extracted from the PDF.", err)
	}
	contents, err := io.ReadAll(io.LimitReader(plainText, maxPDFBytes+1))
	if err != nil {
		return nil, NewError(LoadFailed, "Text could not be extracted from the PDF.", err)
	}
	if int64(len(contents)) > maxPDFBytes || strings.TrimSpace(string(contents)) == "" {
		return nil, NewError(LoadFailed, "The PDF does not contain usable text.", nil)
	}
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = filepath.Base(request.Path)
	}
	return []Document{{Content: string(contents), Metadata: Metadata{
		Source: source, DisplayName: source, Kind: InputPDF, Provenance: mergedProvenance(request.Provenance, map[string]string{"source_uri": canonicalLocalPath(request.Path), "loader": "pdf/v1", "source_kind": "pdf", "content_type": "application/pdf"}),
	}}}, nil
}

// WebLoader fetches one public HTTP(S) resource and extracts non-executing
// visible HTML/text content. It never crawls links or uses environment proxies.
type WebLoader struct {
	Resolver *net.Resolver
	Client   *http.Client
	Options  Options
}

func (WebLoader) Name() string { return "web-url" }
func (WebLoader) Supports(request Request) bool {
	return request.Kind == InputWebURL || (request.Kind == InputAuto && isHTTPURL(request.URL))
}
func (loader WebLoader) Load(ctx context.Context, request Request) ([]Document, error) {
	if !allowsScheme(loader.Options, request.URL) {
		return nil, NewError(PolicyRejected, "The URL scheme is not allowed.", nil)
	}
	parsed, err := parseSafeWebURL(ctx, loader.resolver(), request.URL)
	if err != nil {
		return nil, err
	}
	client := loader.Client
	if client == nil {
		client = newSafeWebClient(loader.resolver())
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, NewError(InvalidInput, "Provide a valid public HTTP(S) URL without credentials.", err)
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if _, ok := CategoryOf(err); ok {
			return nil, err
		}
		return nil, NewError(LoadFailed, "The web page could not be loaded. Please try again later.", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, NewError(LoadFailed, "The web page could not be loaded. Please try again later.", nil)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxWebResponseBytes+1))
	if err != nil {
		return nil, NewError(LoadFailed, "The web page could not be loaded. Please try again later.", err)
	}
	if int64(len(body)) > minPositive(maxWebResponseBytes, effectiveLimits(loader.Options.Limits, request.Limits).SourceBytes) {
		return nil, NewError(LoadFailed, "The web page is too large to ingest.", nil)
	}
	content, err := extractWebContent(response.Header.Get("Content-Type"), body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		return nil, NewError(LoadFailed, "The web page does not contain usable text.", nil)
	}
	source := strings.TrimSpace(request.Source)
	if source == "" {
		source = response.Request.URL.String()
	}
	return []Document{{Content: content, Metadata: Metadata{
		Source: source, DisplayName: source, Kind: InputWebURL, Provenance: mergedProvenance(request.Provenance, map[string]string{"source_uri": redactURL(response.Request.URL), "loader": "web/v1", "source_kind": "web_url", "content_type": response.Header.Get("Content-Type")}),
	}}}, nil
}

func (loader WebLoader) resolver() *net.Resolver {
	if loader.Resolver != nil {
		return loader.Resolver
	}
	return net.DefaultResolver
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Host != ""
}

func parseSafeWebURL(ctx context.Context, resolver *net.Resolver, value string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) || parsed.User != nil {
		return nil, NewError(InvalidInput, "Provide a valid public HTTP(S) URL without credentials.", err)
	}
	if err := validatePublicHost(ctx, resolver, parsed.Hostname()); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validatePublicHost(ctx context.Context, resolver *net.Resolver, host string) error {
	if host == "" {
		return NewError(InvalidInput, "Provide a valid public HTTP(S) URL without credentials.", nil)
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddress(address) {
			return NewError(UnavailableInput, "The URL destination is not publicly reachable.", nil)
		}
		return nil
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return NewError(UnavailableInput, "The URL destination could not be resolved.", err)
	}
	for _, address := range addresses {
		if !isPublicAddress(address) {
			return NewError(UnavailableInput, "The URL destination is not publicly reachable.", nil)
		}
	}
	return nil
}

func isPublicAddress(address netip.Addr) bool {
	return address.IsValid() && !address.IsLoopback() && !address.IsPrivate() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsMulticast() && !address.IsUnspecified()
}

func newSafeWebClient(resolver *net.Resolver) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext(resolver, dialer),
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ForceAttemptHTTP2:     false,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   webRequestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			// via includes the initial request, so allowing three redirects means
			// rejecting the attempt to follow a fourth redirect.
			if len(via) >= maxWebRedirects+1 {
				return NewError(LoadFailed, "The URL redirected too many times.", nil)
			}
			_, err := parseSafeWebURL(request.Context(), resolver, request.URL.String())
			return err
		},
	}
}

func safeDialContext(resolver *net.Resolver, dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve destination: %w", err)
		}
		for _, candidate := range addresses {
			if !isPublicAddress(candidate) {
				return nil, errors.New("destination is not public")
			}
		}
		var lastError error
		for _, candidate := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if err == nil {
				return connection, nil
			}
			lastError = err
		}
		return nil, lastError
	}
}

func extractWebContent(contentType string, body []byte) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "" || mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return extractHTMLText(strings.NewReader(string(body))), nil
	}
	if strings.HasPrefix(mediaType, "text/") {
		return strings.Join(strings.Fields(string(body)), " "), nil
	}
	return "", NewError(LoadFailed, "The URL did not return supported text or HTML content.", nil)
}

func extractHTMLText(reader io.Reader) string {
	tokenizer := html.NewTokenizer(reader)
	var text []string
	skipDepth := 0
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			return strings.Join(text, " ")
		case html.StartTagToken:
			tag, _ := tokenizer.TagName()
			if isExcludedHTMLTag(string(tag)) {
				skipDepth++
			}
		case html.EndTagToken:
			tag, _ := tokenizer.TagName()
			if isExcludedHTMLTag(string(tag)) && skipDepth > 0 {
				skipDepth--
			}
		case html.TextToken:
			if skipDepth == 0 {
				text = append(text, strings.Fields(string(tokenizer.Text()))...)
			}
		}
	}
}

func isExcludedHTMLTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "script", "style", "noscript", "svg", "nav", "footer", "header", "aside", "form", "iframe", "template", "head":
		return true
	default:
		return false
	}
}

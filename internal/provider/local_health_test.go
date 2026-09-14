package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalProbeRequiresPreparedModelAndIdentity(t *testing.T) {
	for _, test := range []struct {
		name, service, schema, model string
		valid                        bool
	}{
		{"正确模型", "local-rag-sidecar", "1.0", "configured", true},
		{"错误身份", "other", "1.0", "configured", false},
		{"错误协议", "local-rag-sidecar", "2.0", "configured", false},
		{"未加载模型", "local-rag-sidecar", "1.0", "other", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"service": test.service, "schema_version": test.schema, "status": "ok", "models": map[string]string{"embedding": test.model, "rerank": test.model}})
			}))
			defer server.Close()
			for _, probe := range []func(context.Context) error{NewLocalEmbedProvider(server.URL, "configured", 512).Probe, NewLocalRerankProvider(server.URL, "configured").Probe} {
				if err := probe(context.Background()); (err == nil) != test.valid {
					t.Fatalf("有效=%v，错误=%v", test.valid, err)
				}
			}
		})
	}
}

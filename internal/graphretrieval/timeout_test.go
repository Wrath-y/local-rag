package graphretrieval

import (
	"context"
	"testing"
	"time"
)

type stalledEmbedding struct{ calls *int }

func (s stalledEmbedding) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	*s.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFailedEmbeddingKeepsBM25AndSkipsMissingGeneration(t *testing.T) {
	calls := 0
	service := retrievalService(usableView())
	service.Embedder.Provider = stalledEmbedding{calls: &calls}
	service.ProviderTimeout = time.Millisecond
	out, e := service.Retrieve(context.Background(), "ns", Request{Query: "alpha"})
	if e != nil || out.ModeUsed != "bm25_only" || !out.Degraded || calls != 1 {
		t.Fatalf("超时未保留BM25 %+v %v calls=%d", out, e, calls)
	}
	v := usableView()
	v.vectorState = StageUnavailable
	service.Repository = serviceRepository{view: v}
	calls = 0
	out, e = service.Retrieve(context.Background(), "ns", Request{Query: "alpha"})
	if e != nil || out.ModeUsed != "bm25_only" || calls != 0 {
		t.Fatalf("缺失向量仍调用Provider %+v %v calls=%d", out, e, calls)
	}
}

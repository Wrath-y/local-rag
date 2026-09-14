package graphretrieval

import (
	"context"
	"github.com/Wrath-y/local-rag/internal/graphquery"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/metadatafilter"
)

// MetadataCandidates 必须在候选排序与 LIMIT 之前下推；不接受最终命中过滤替代。
type MetadataCandidates interface {
	WithMetadata(metadatafilter.Filter) ReadView
}

type metadataReadView struct {
	ReadView
	graph *graphquery.MetadataView
}

func (v metadataReadView) Nodes(ctx context.Context, ids []string) ([]graphsnapshot.Node, error) {
	return v.graph.Nodes(ctx, ids)
}
func (v metadataReadView) Adjacency(ctx context.Context, ids []string, d graphquery.Direction) ([]graphsnapshot.Edge, error) {
	return v.graph.Adjacency(ctx, ids, d)
}

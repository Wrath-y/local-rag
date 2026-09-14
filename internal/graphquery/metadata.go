package graphquery

import (
	"context"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/metadatafilter"
)

// MetadataView 在扩展前隐藏不合法节点及其关联边，并保留首次读取错误。
type MetadataView struct {
	ReadView
	Filter metadatafilter.Filter
	Err    error
}

func (v *MetadataView) Nodes(ctx context.Context, ids []string) ([]graphsnapshot.Node, error) {
	nodes, e := v.ReadView.Nodes(ctx, ids)
	if e != nil {
		v.Err = e
		return nil, e
	}
	out := []graphsnapshot.Node{}
	for _, n := range nodes {
		if v.Filter.Matches(n.Properties) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (v *MetadataView) Adjacency(ctx context.Context, ids []string, d Direction) ([]graphsnapshot.Edge, error) {
	edges, e := v.ReadView.Adjacency(ctx, ids, d)
	if e != nil {
		v.Err = e
		return nil, e
	}
	ends := []string{}
	for _, edge := range edges {
		if v.Filter.Matches(edge.Properties) {
			ends = append(ends, edge.From, edge.To)
		}
	}
	nodes, e := v.Nodes(ctx, sortedUnique(ends))
	if e != nil {
		return nil, e
	}
	allowed := map[string]bool{}
	for _, n := range nodes {
		allowed[n.ID] = true
	}
	out := []graphsnapshot.Edge{}
	for _, edge := range edges {
		if allowed[edge.From] && allowed[edge.To] && v.Filter.Matches(edge.Properties) {
			out = append(out, edge)
		}
	}
	return out, nil
}

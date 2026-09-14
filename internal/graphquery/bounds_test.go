package graphquery

import (
	"context"
	"fmt"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"slices"
	"testing"
)

func TestDensePathsHaveExplicitWorkBudget(t *testing.T) {
	v := fakeView{version: "v1", hash: "hash", nodes: map[string]graphsnapshot.Node{}}
	for i := 0; i < 20; i++ {
		id := fmt.Sprint(i)
		v.nodes[id] = queryNode(id, "person")
		for j := 0; j < 20; j++ {
			if i != j {
				v.edges = append(v.edges, queryEdge(fmt.Sprintf("%d-%d", i, j), id, fmt.Sprint(j), "explicit", "1"))
			}
		}
	}
	depth := 6
	out, e := (Service{Repository: fakeRepository{view: v}}).Paths(context.Background(), "book", PathsRequest{SourceNodeIDs: []string{"0"}, TargetNodeIDs: []string{"19"}, MaxDepth: &depth})
	if e != nil || !out.Truncated || !slices.Contains(out.TruncationReasons, MaxWorkItemsReached) || len(out.Paths) > DefaultMaxPaths {
		t.Fatalf("密集路径未有界截断 paths=%d reasons=%v e=%v", len(out.Paths), out.TruncationReasons, e)
	}
	one := 1
	if _, e := NormalizeTraverse(TraverseRequest{StartNodeIDs: []string{"0", "1"}, MaxNodes: &one}); e == nil {
		t.Fatal("起点数量突破节点上限")
	}
}

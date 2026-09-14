package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wrath-y/local-rag/internal/graphquery"
	"github.com/Wrath-y/local-rag/internal/graphretrieval"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/metadatafilter"
)

func TestMetadataCandidatePushdownAndAllEvidence(t *testing.T) {
	s, e := New(t.TempDir()+"/rag.db", 2)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	for _, ns := range []string{"novel-one-main", "novel-one-fork", "novel-two-main", "novel-two-fork"} {
		nodes := []graphsnapshot.Node{}
		edges := []graphsnapshot.Edge{}
		props := func(from int, to any, confirmation string) json.RawMessage {
			v, _ := json.Marshal(map[string]any{"branch_id": ns, "confirmation": confirmation, "valid_from_order": from, "valid_to_order": to})
			return v
		}
		for _, n := range []struct {
			id           string
			from         int
			to           any
			confirmation string
		}{{"a-future", 3, nil, "confirmed"}, {"b-expired", 0, 2, "confirmed"}, {"c-planned", 0, nil, "planned"}, {"x-good", 0, nil, "confirmed"}, {"y-good", 2, 3, "confirmed"}} {
			nodes = append(nodes, graphsnapshot.Node{ID: n.id, Type: "person", Label: "小舟", Text: "alpha 小舟 " + ns, Properties: props(n.from, n.to, n.confirmation), Provenance: []byte(`{}`)})
		}
		for i, pair := range [][2]string{{"x-good", "y-good"}, {"x-good", "a-future"}, {"a-future", "y-good"}, {"x-good", "b-expired"}, {"x-good", "c-planned"}} {
			edges = append(edges, graphsnapshot.Edge{ID: fmt.Sprint(i), From: pair[0], To: pair[1], Type: "knows", RelationKind: "explicit", Confidence: "1", Properties: props(0, nil, "confirmed"), Provenance: []byte(`{}`)})
		}
		_, hash, e := graphsnapshot.CanonicalHash(nodes, edges)
		if e != nil {
			t.Fatal(e)
		}
		task := "task-" + ns
		service := graphsnapshot.NewService(s, func() (string, error) { return task, nil })
		if _, e := service.Put(ctx, ns, "v1", graphsnapshot.Request{SchemaVersion: "1.0", Mode: "full", ContentHash: hash, Nodes: nodes, Edges: edges}); e != nil {
			t.Fatal(e)
		}
		for _, fn := range []func() error{func() error { return s.PromoteGraphComponent(ctx, task) }, func() error { return s.PopulateGraphSearchDocuments(ctx, task) }, func() error { return s.BuildGraphVectors(ctx, task, retrievalEmbedder{}) }} {
			if e := fn(); e != nil {
				t.Fatal(e)
			}
		}
		f := metadatafilter.Filter{All: []metadatafilter.Condition{{Field: "branch_id", Op: "eq", Value: ns}, {Field: "confirmation", Op: "eq", Value: "confirmed"}, {Field: "valid_from_order", Op: "lte", Value: 2}}, Any: [][]metadatafilter.Condition{{{Field: "valid_to_order", Op: "is_null"}, {Field: "valid_to_order", Op: "gt", Value: 2}}}}
		e = s.WithRetrievalRead(ctx, ns, "v1", func(v graphretrieval.ReadView) error {
			v = v.(graphretrieval.MetadataCandidates).WithMetadata(f)
			for _, vector := range []bool{false, true} {
				var got []graphretrieval.Candidate
				var state graphretrieval.StageOutcome
				var e error
				if vector {
					got, state, e = v.Vector(ctx, []float32{1, 0}, nil, 1)
				} else {
					got, state, e = v.BM25(ctx, "alpha", nil, 1)
				}
				if e != nil || state != graphretrieval.StageUsed || len(got) != 1 || got[0].NodeID != "x-good" {
					t.Fatalf("候选下推 vector=%v: %+v %s %v", vector, got, state, e)
				}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		result, ge := (graphretrieval.Service{Repository: s}).Retrieve(ctx, ns, graphretrieval.Request{Query: "alpha", SnapshotVersion: "v1", Filters: f})
		if ge != nil {
			t.Fatal(ge)
		}
		if len(result.Results) != 2 {
			t.Fatalf("召回不足: %+v", result)
		}
		for _, r := range result.Results {
			if !f.Matches(r.Node.Properties) {
				t.Fatal("结果越界")
			}
			for _, n := range r.Evidence.Path.Nodes {
				if !f.Matches(n.Properties) {
					t.Fatal("路径节点越界")
				}
			}
			for _, edge := range r.Evidence.Path.Edges {
				if !f.Matches(edge.Properties) {
					t.Fatal("路径边越界")
				}
			}
		}
		g := graphquery.Service{Repository: s}
		tr, ge := g.Traverse(ctx, ns, graphquery.TraverseRequest{SnapshotVersion: "v1", StartNodeIDs: []string{"x-good"}, Filters: f})
		if ge != nil || len(tr.Nodes) != 2 || len(tr.Edges) != 1 {
			t.Fatalf("遍历 %+v %v", tr, ge)
		}
		paths, ge := g.Paths(ctx, ns, graphquery.PathsRequest{SnapshotVersion: "v1", SourceNodeIDs: []string{"x-good"}, TargetNodeIDs: []string{"y-good"}, Filters: f})
		if ge != nil || len(paths.Paths) != 1 || len(paths.Nodes) != 2 || len(paths.Edges) != 1 {
			t.Fatalf("路径 %+v %v", paths, ge)
		}
		if _, ge = g.Paths(ctx, ns, graphquery.PathsRequest{SnapshotVersion: "v1", SourceNodeIDs: []string{"a-future"}, TargetNodeIDs: []string{"y-good"}, Filters: f}); ge == nil {
			t.Fatal("未来端点未拒绝")
		}
	}
}

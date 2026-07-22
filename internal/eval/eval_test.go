package eval

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/store"
)

func goldenSuite(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "evaluation", "fixtures", "golden-v1")
}

func TestLoadDatasetGoldenFixture(t *testing.T) {
	dataset, err := LoadDataset(goldenSuite(t))
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Manifest.Version != "golden-v1" || len(dataset.Corpus) != 4 || len(dataset.Cases) != 3 {
		t.Fatalf("unexpected suite: %#v", dataset)
	}
}

func TestLoadDatasetRejectsDuplicateCaseID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"schema_version":"retrieval-evaluation/v1","version":"test","corpus":"corpus.jsonl","cases":"cases.jsonl"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corpus.jsonl"), []byte(`{"id":"chunk","text":"text","source":"source","embedding":[1,0]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := `{"id":"duplicate","query":"one","embedding":[1,0],"targets":[{"chunk_id":"chunk"}]}` + "\n" + `{"id":"duplicate","query":"two","embedding":[1,0],"targets":[{"chunk_id":"chunk"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "cases.jsonl"), []byte(cases), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDataset(dir)
	if err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("got %v, want duplicate-ID validation error", err)
	}
}

func TestMetricCalculation(t *testing.T) {
	c := Case{ID: "case", Query: "query", Targets: []Target{{ChunkID: "good", Grade: 2}, {ChunkID: "also-good", Grade: 1}}}
	results := []store.RetrieveResult{{ID: 1, Source: "wrong"}, {ID: 2, Source: "right"}, {ID: 3, Source: "right"}}
	got := scoreCase(c, results, map[int64]string{1: "bad", 2: "good", 3: "also-good"}, map[string]string{"good": "right", "also-good": "right"})
	if got.RecallAtK != 1 || got.ReciprocalRank != 0.5 || !got.SourceHit {
		t.Fatalf("unexpected basic metrics: %#v", got)
	}
	if got.NDCG <= 0 || got.NDCG >= 1 {
		t.Fatalf("expected discounted NDCG between zero and one, got %v", got.NDCG)
	}
}

func TestRunDatasetIsDeterministicAndRetrievalOnly(t *testing.T) {
	dataset, err := LoadDataset(goldenSuite(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(goldenSuite(t), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := RunDataset(context.Background(), dataset, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunDataset(context.Background(), dataset, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replay differs:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.Metrics != (Metrics{RecallAtK: 1, MRR: 1, NDCG: 1, SourceHitRate: 1}) {
		t.Fatalf("unexpected golden metrics: %#v", first.Metrics)
	}
}

func TestCompareFlagsMetricDrop(t *testing.T) {
	baseline := Baseline{Metrics: Metrics{RecallAtK: 1, MRR: 1, NDCG: 1, SourceHitRate: 1}}
	run := Run{Metrics: Metrics{RecallAtK: 0.9, MRR: 1, NDCG: 1, SourceHitRate: 1}}
	comparison := Compare(baseline, run, Tolerances{MaxDrop: map[string]float64{"recall_at_k": 0.05}})
	if len(comparison.Failures) != 1 || comparison.Failures[0].Metric != "recall_at_k" {
		t.Fatalf("unexpected comparison: %#v", comparison)
	}
}

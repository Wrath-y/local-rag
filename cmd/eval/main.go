// Command eval runs the deterministic retrieval-only regression suite.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/eval"
)

func main() {
	suite := flag.String("suite", "evaluation/fixtures/golden-v1", "directory containing manifest.json and JSONL fixtures")
	configPath := flag.String("config", "evaluation/fixtures/golden-v1/config.yaml", "deterministic retrieval configuration")
	baselinePath := flag.String("baseline", "evaluation/fixtures/golden-v1/baseline.json", "approved baseline JSON")
	tolerancesPath := flag.String("tolerances", "evaluation/fixtures/golden-v1/tolerances.json", "metric tolerance JSON")
	output := flag.String("output", "artifacts/retrieval-evaluation.json", "result artifact path")
	updateBaseline := flag.Bool("update-baseline", false, "replace the approved baseline from this reviewed run")
	approve := flag.String("approve", "", "required acknowledgement for --update-baseline; use the suite version")
	flag.Parse()

	dataset, err := eval.LoadDataset(*suite)
	failIf(err)
	cfg, err := config.Load(*configPath)
	failIf(err)
	run, err := eval.RunDataset(context.Background(), dataset, cfg)
	failIf(err)
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		failIf(err)
	}
	failIf(eval.WriteRun(*output, run))

	if *updateBaseline {
		if *approve != dataset.Manifest.Version {
			failIf(fmt.Errorf("refusing baseline update: --approve must equal suite version %q", dataset.Manifest.Version))
		}
		failIf(eval.WriteBaseline(*baselinePath, run))
		fmt.Printf("approved baseline updated: %s (artifact: %s)\n", *baselinePath, *output)
		return
	}

	baseline, err := eval.LoadBaseline(*baselinePath)
	failIf(err)
	tolerances, err := eval.LoadTolerances(*tolerancesPath)
	failIf(err)
	comparison := eval.Compare(baseline, run, tolerances)
	if message := comparison.Error(); message != "" {
		fmt.Fprintf(os.Stderr, "retrieval regression: %s\n", message)
		for _, result := range run.Cases {
			fmt.Fprintf(os.Stderr, "case=%s recall@k=%.6f mrr=%.6f ndcg=%.6f source_hit=%t targets=%v evidence=%v\n", result.CaseID, result.RecallAtK, result.ReciprocalRank, result.NDCG, result.SourceHit, result.Targets, result.Evidence)
		}
		fmt.Fprintf(os.Stderr, "result artifact preserved at %s\n", *output)
		os.Exit(1)
	}
	fmt.Printf("retrieval evaluation passed: recall@k=%.6f mrr=%.6f ndcg=%.6f source_hit_rate=%.6f (artifact: %s)\n", run.Metrics.RecallAtK, run.Metrics.MRR, run.Metrics.NDCG, run.Metrics.SourceHitRate, *output)
}

func failIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "evaluation failed:", err)
		os.Exit(2)
	}
}

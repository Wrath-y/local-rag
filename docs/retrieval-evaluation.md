# Retrieval evaluation

`cmd/eval` evaluates ranked retrieval only. It creates a temporary SQLite fixture store and invokes the shared production retrieval composition; it does not open production storage, generate an answer, or call an LLM judge.

## Run locally or in CI

```bash
go run ./cmd/eval
```

The command validates `evaluation/fixtures/golden-v1`, writes a replayable result snapshot to `artifacts/retrieval-evaluation.json`, and compares its Recall@K, MRR, nDCG, and source-hit rate with the approved baseline. A metric may fall only by the corresponding absolute `max_drop` in `tolerances.json`; otherwise the command exits non-zero. Failure output includes the baseline, observed value, threshold, every query's targets, and ranked evidence. The result artifact is retained on either outcome.

Pass `--suite`, `--config`, `--baseline`, `--tolerances`, and `--output` to run another deterministic suite. The configuration snapshot in every output records embedding dimensions/prefix, Top-K, candidate multiplier, score weights, and rerank state. Baselines also retain the fixture version and SHA-256 digests of both JSONL files.

## Fixture format and evolution

Each suite has a `manifest.json`, a `corpus.jsonl`, and a `cases.jsonl`. The schemas in `evaluation/schemas/` document the version-1 records; the Go loader enforces the same constraints before retrieval begins.

- Corpus records require a stable chunk `id`, `text`, `source`, and deterministic `embedding`.
- Cases require a stable `id`, `query`, matching deterministic `embedding`, one or more targets, and optional labels.
- A target must resolve to a fixture `chunk_id` and/or a fixture `source`; `grade` defaults to 1.
- Do not alter a published case ID or silently relabel a case. Add a new versioned suite when changes affect relevance intent or fixture embeddings.
- Reviewers must inspect fixture, label, tolerance, and baseline changes together. A changed JSONL digest deliberately makes the baseline provenance visible.

Keep the CI suite small, local, and deterministic. Larger or provider-backed evaluations belong in scheduled/manual jobs and must not replace the deterministic gate.

## Approving a new baseline

First run the suite and review the produced artifact and diff. A maintainer then performs the explicit replacement below, where the acknowledgement must exactly match the manifest version:

```bash
go run ./cmd/eval --output artifacts/retrieval-evaluation.json \
  --update-baseline --approve golden-v1
```

Commit the reviewed `baseline.json` with its fixture/configuration changes. `--update-baseline` never runs implicitly in the CI comparison path.

// Package eval implements deterministic, retrieval-only offline evaluation.
package eval

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/retrieval"
	"github.com/Wrath-y/local-rag/internal/store"
)

const SchemaVersion = "retrieval-evaluation/v1"

// Manifest selects the versioned JSONL corpus and cases that make up a suite.
type Manifest struct {
	SchemaVersion string `json:"schema_version"`
	Version       string `json:"version"`
	Corpus        string `json:"corpus"`
	Cases         string `json:"cases"`
}

// Chunk is a deterministic fixture-corpus record.
type Chunk struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"`
	Embedding []float32 `json:"embedding"`
}

// Target identifies a relevant fixture chunk or source. Grade defaults to one.
type Target struct {
	ChunkID string `json:"chunk_id,omitempty"`
	Source  string `json:"source,omitempty"`
	Grade   int    `json:"grade,omitempty"`
}

// Case is one relevance-labelled query in a suite.
type Case struct {
	ID        string    `json:"id"`
	Query     string    `json:"query"`
	Embedding []float32 `json:"embedding"`
	Targets   []Target  `json:"targets"`
	Labels    []string  `json:"labels,omitempty"`
}

// Dataset is a validated immutable evaluation suite.
type Dataset struct {
	Manifest     Manifest
	Corpus       []Chunk
	Cases        []Case
	CorpusDigest string
	CasesDigest  string
}

// ConfigurationSnapshot captures all knobs that influence ranked output.
type ConfigurationSnapshot struct {
	EmbeddingDims       int     `json:"embedding_dims"`
	QueryPrefix         string  `json:"query_prefix"`
	TopK                int     `json:"top_k"`
	CandidateMultiplier int     `json:"candidate_multiplier"`
	VectorWeight        float64 `json:"vector_weight"`
	BM25Weight          float64 `json:"bm25_weight"`
	RerankEnabled       bool    `json:"rerank_enabled"`
}

// Metrics contains the aggregate standard ranked-retrieval metrics.
type Metrics struct {
	RecallAtK     float64 `json:"recall_at_k"`
	MRR           float64 `json:"mrr"`
	NDCG          float64 `json:"ndcg"`
	SourceHitRate float64 `json:"source_hit_rate"`
}

// Evidence is one ranked result, retaining scores needed to diagnose changes.
type Evidence struct {
	Rank        int     `json:"rank"`
	ChunkID     string  `json:"chunk_id"`
	Source      string  `json:"source"`
	Text        string  `json:"text"`
	VectorScore float64 `json:"vector_score"`
	BM25Score   float64 `json:"bm25_score"`
	FinalScore  float64 `json:"final_score"`
}

// CaseResult records a query, its labels, metrics, and complete ranked evidence.
type CaseResult struct {
	CaseID         string     `json:"case_id"`
	Query          string     `json:"query"`
	Targets        []Target   `json:"targets"`
	RecallAtK      float64    `json:"recall_at_k"`
	ReciprocalRank float64    `json:"reciprocal_rank"`
	NDCG           float64    `json:"ndcg"`
	SourceHit      bool       `json:"source_hit"`
	Evidence       []Evidence `json:"evidence"`
}

// Run is a deterministic result snapshot. It contains no wall-clock fields so
// identical input replays byte-for-byte after JSON formatting.
type Run struct {
	SchemaVersion string                `json:"schema_version"`
	Dataset       DatasetSnapshot       `json:"dataset"`
	Configuration ConfigurationSnapshot `json:"configuration"`
	Metrics       Metrics               `json:"metrics"`
	Cases         []CaseResult          `json:"cases"`
}

type DatasetSnapshot struct {
	Version      string `json:"version"`
	CorpusDigest string `json:"corpus_digest"`
	CasesDigest  string `json:"cases_digest"`
}

// Baseline is a reviewed run accepted for regression comparisons.
type Baseline struct {
	SchemaVersion string                `json:"schema_version"`
	Dataset       DatasetSnapshot       `json:"dataset"`
	Configuration ConfigurationSnapshot `json:"configuration"`
	Metrics       Metrics               `json:"metrics"`
}

// Tolerances specifies permitted absolute metric drops from the baseline.
type Tolerances struct {
	SchemaVersion string             `json:"schema_version"`
	MaxDrop       map[string]float64 `json:"max_drop"`
}

// Comparison reports aggregate regressions. A non-empty Failures list fails CI.
type Comparison struct {
	Failures []MetricFailure `json:"failures,omitempty"`
}

type MetricFailure struct {
	Metric    string  `json:"metric"`
	Baseline  float64 `json:"baseline"`
	Observed  float64 `json:"observed"`
	Threshold float64 `json:"threshold"`
}

// LoadDataset validates a manifest and all JSONL records before any evaluation
// work starts. Error messages name the offending line or case.
func LoadDataset(dir string) (*Dataset, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion || strings.TrimSpace(manifest.Version) == "" || manifest.Corpus == "" || manifest.Cases == "" {
		return nil, fmt.Errorf("manifest: require schema_version %q, version, corpus, and cases", SchemaVersion)
	}
	corpusPath := filepath.Join(dir, manifest.Corpus)
	casesPath := filepath.Join(dir, manifest.Cases)
	corpus, corpusDigest, err := loadJSONL[Chunk](corpusPath)
	if err != nil {
		return nil, fmt.Errorf("fixture corpus: %w", err)
	}
	cases, casesDigest, err := loadJSONL[Case](casesPath)
	if err != nil {
		return nil, fmt.Errorf("cases: %w", err)
	}
	if err := validateDataset(corpus, cases); err != nil {
		return nil, err
	}
	return &Dataset{Manifest: manifest, Corpus: corpus, Cases: cases, CorpusDigest: corpusDigest, CasesDigest: casesDigest}, nil
}

func loadJSONL[T any](path string) ([]T, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	buf := make([]byte, 0, 1024)
	scanner.Buffer(buf, 1024*1024)
	var records []T
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			return nil, "", fmt.Errorf("line %d: blank lines are not allowed", line)
		}
		var record T
		if err := decodeStrict(scanner.Bytes(), &record); err != nil {
			return nil, "", fmt.Errorf("line %d: %w", line, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, "", err
	}
	if len(records) == 0 {
		return nil, "", fmt.Errorf("must contain at least one record")
	}
	sum := sha256.Sum256(b)
	return records, hex.EncodeToString(sum[:]), nil
}

func validateDataset(corpus []Chunk, cases []Case) error {
	chunkIDs, sources := map[string]bool{}, map[string]bool{}
	dims := 0
	for i, chunk := range corpus {
		if chunk.ID == "" || chunk.Text == "" || chunk.Source == "" || len(chunk.Embedding) == 0 {
			return fmt.Errorf("fixture corpus record %d: require id, text, source, and embedding", i+1)
		}
		if chunkIDs[chunk.ID] {
			return fmt.Errorf("fixture corpus record %d: duplicate id %q", i+1, chunk.ID)
		}
		if dims == 0 {
			dims = len(chunk.Embedding)
		}
		if len(chunk.Embedding) != dims {
			return fmt.Errorf("fixture corpus record %d: embedding dimensions differ", i+1)
		}
		chunkIDs[chunk.ID], sources[chunk.Source] = true, true
	}
	caseIDs := map[string]bool{}
	for i, c := range cases {
		if c.ID == "" || c.Query == "" || len(c.Embedding) != dims || len(c.Targets) == 0 {
			return fmt.Errorf("case %d: require id, query, matching embedding dimensions, and at least one target", i+1)
		}
		if caseIDs[c.ID] {
			return fmt.Errorf("case %d: duplicate id %q", i+1, c.ID)
		}
		caseIDs[c.ID] = true
		for _, target := range c.Targets {
			if target.ChunkID == "" && target.Source == "" {
				return fmt.Errorf("case %q: relevance target must contain chunk_id or source", c.ID)
			}
			if target.ChunkID != "" && !chunkIDs[target.ChunkID] {
				return fmt.Errorf("case %q: target chunk %q does not resolve", c.ID, target.ChunkID)
			}
			if target.Source != "" && !sources[target.Source] {
				return fmt.Errorf("case %q: target source %q does not resolve", c.ID, target.Source)
			}
			if target.Grade < 0 {
				return fmt.Errorf("case %q: relevance grade cannot be negative", c.ID)
			}
		}
	}
	return nil
}

// SnapshotConfiguration records the retrieval-affecting configuration only.
func SnapshotConfiguration(cfg *config.Config, rerankEnabled bool) ConfigurationSnapshot {
	return ConfigurationSnapshot{EmbeddingDims: cfg.Embedding.Dims, QueryPrefix: cfg.Embedding.QueryPrefix, TopK: cfg.Retrieve.TopK, CandidateMultiplier: cfg.Retrieve.CandidateMultiplier, VectorWeight: cfg.Retrieve.ScoreWeights.Vector, BM25Weight: cfg.Retrieve.ScoreWeights.BM25, RerankEnabled: rerankEnabled}
}

// RunDataset creates a temporary fixture-only store and calls the same ranked
// retrieval Service used by HTTP and hook requests. It never opens production
// storage, calls an LLM, or mutates supplied configuration.
func RunDataset(ctx context.Context, dataset *Dataset, cfg *config.Config) (Run, error) {
	if dataset == nil || cfg == nil {
		return Run{}, fmt.Errorf("dataset and configuration are required")
	}
	if cfg.Embedding.Dims == 0 {
		cfg = cloneConfig(cfg)
		cfg.Embedding.Dims = len(dataset.Corpus[0].Embedding)
	}
	if cfg.Embedding.Dims != len(dataset.Corpus[0].Embedding) {
		return Run{}, fmt.Errorf("configuration embedding dimensions do not match fixture corpus")
	}
	tmp, err := os.MkdirTemp("", "rag-retrieval-eval-*")
	if err != nil {
		return Run{}, fmt.Errorf("create fixture store: %w", err)
	}
	defer os.RemoveAll(tmp)
	st, err := store.New(filepath.Join(tmp, "fixture.db"), cfg.Embedding.Dims)
	if err != nil {
		return Run{}, err
	}
	defer st.Close()
	idMap := make(map[int64]string, len(dataset.Corpus))
	chunkSources := make(map[string]string, len(dataset.Corpus))
	for _, chunk := range dataset.Corpus {
		id, insertErr := st.InsertChunk(chunk.Text, chunk.Source, "fixture:"+chunk.ID, "", "", chunk.Embedding)
		if insertErr != nil {
			return Run{}, fmt.Errorf("insert fixture chunk %q: %w", chunk.ID, insertErr)
		}
		idMap[id] = chunk.ID
		chunkSources[chunk.ID] = chunk.Source
	}
	embedder := fixtureEmbedder{dims: cfg.Embedding.Dims, vectors: make(map[string][]float32, len(dataset.Cases))}
	for _, c := range dataset.Cases {
		embedder.vectors[cfg.Embedding.QueryPrefix+c.Query] = c.Embedding
	}
	svc := retrieval.Service{Config: cfg, Embedder: embedder, Stores: func(fn func(*store.Store) error) error { return fn(st) }}
	run := Run{SchemaVersion: SchemaVersion, Dataset: DatasetSnapshot{Version: dataset.Manifest.Version, CorpusDigest: dataset.CorpusDigest, CasesDigest: dataset.CasesDigest}, Configuration: SnapshotConfiguration(cfg, false)}
	for _, c := range dataset.Cases {
		results, retrieveErr := svc.Retrieve(ctx, c.Query, 0)
		if retrieveErr != nil {
			return Run{}, fmt.Errorf("case %q: %w", c.ID, retrieveErr)
		}
		caseResult := scoreCase(c, results, idMap, chunkSources)
		run.Cases = append(run.Cases, caseResult)
	}
	run.Metrics = aggregate(run.Cases)
	return run, nil
}

type fixtureEmbedder struct {
	dims    int
	vectors map[string][]float32
}

func (e fixtureEmbedder) Dims() int { return e.dims }
func (e fixtureEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		vector, ok := e.vectors[text]
		if !ok {
			return nil, fmt.Errorf("fixture has no deterministic embedding for query %q", text)
		}
		result[i] = append([]float32(nil), vector...)
	}
	return result, nil
}

func scoreCase(c Case, results []store.RetrieveResult, idMap map[int64]string, chunkSources map[string]string) CaseResult {
	out := CaseResult{CaseID: c.ID, Query: c.Query, Targets: c.Targets}
	matchedTargets := make([]bool, len(c.Targets))
	relGrades := make([]int, 0, len(results))
	expectedSources := map[string]bool{}
	for _, target := range c.Targets {
		if target.Source != "" {
			expectedSources[target.Source] = true
		}
		if target.ChunkID != "" {
			expectedSources[chunkSources[target.ChunkID]] = true
		}
	}
	for i, result := range results {
		chunkID := idMap[result.ID]
		evidence := Evidence{Rank: i + 1, ChunkID: chunkID, Source: result.Source, Text: result.Text, VectorScore: result.VecScore, BM25Score: result.BM25Score, FinalScore: result.FinalScore}
		out.Evidence = append(out.Evidence, evidence)
		bestGrade := 0
		for j, target := range c.Targets {
			if (target.ChunkID != "" && target.ChunkID == chunkID) || (target.Source != "" && target.Source == result.Source) {
				matchedTargets[j] = true
				grade := target.Grade
				if grade == 0 {
					grade = 1
				}
				if grade > bestGrade {
					bestGrade = grade
				}
			}
		}
		if expectedSources[result.Source] {
			out.SourceHit = true
		}
		relGrades = append(relGrades, bestGrade)
		if bestGrade > 0 && out.ReciprocalRank == 0 {
			out.ReciprocalRank = 1 / float64(i+1)
		}
	}
	matched := 0
	for _, found := range matchedTargets {
		if found {
			matched++
		}
	}
	out.RecallAtK = float64(matched) / float64(len(c.Targets))
	out.NDCG = ndcg(relGrades, c.Targets, len(results))
	return out
}

func ndcg(grades []int, targets []Target, k int) float64 {
	dcg := 0.0
	for i, grade := range grades {
		if grade > 0 {
			dcg += (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(i)+2)
		}
	}
	ideal := make([]int, len(targets))
	for i, target := range targets {
		ideal[i] = target.Grade
		if ideal[i] == 0 {
			ideal[i] = 1
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ideal)))
	idcg := 0.0
	for i, grade := range ideal {
		if i >= k {
			break
		}
		idcg += (math.Pow(2, float64(grade)) - 1) / math.Log2(float64(i)+2)
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func aggregate(cases []CaseResult) Metrics {
	if len(cases) == 0 {
		return Metrics{}
	}
	var metrics Metrics
	for _, c := range cases {
		metrics.RecallAtK += c.RecallAtK
		metrics.MRR += c.ReciprocalRank
		metrics.NDCG += c.NDCG
		if c.SourceHit {
			metrics.SourceHitRate++
		}
	}
	n := float64(len(cases))
	metrics.RecallAtK /= n
	metrics.MRR /= n
	metrics.NDCG /= n
	metrics.SourceHitRate /= n
	return metrics
}

// Compare applies metric-specific maximum-drop tolerances to a completed run.
func Compare(baseline Baseline, observed Run, tolerances Tolerances) Comparison {
	values := []struct {
		name          string
		base, current float64
	}{{"recall_at_k", baseline.Metrics.RecallAtK, observed.Metrics.RecallAtK}, {"mrr", baseline.Metrics.MRR, observed.Metrics.MRR}, {"ndcg", baseline.Metrics.NDCG, observed.Metrics.NDCG}, {"source_hit_rate", baseline.Metrics.SourceHitRate, observed.Metrics.SourceHitRate}}
	comparison := Comparison{}
	for _, value := range values {
		drop, configured := tolerances.MaxDrop[value.name]
		if !configured {
			continue
		}
		threshold := value.base - drop
		if value.current < threshold {
			comparison.Failures = append(comparison.Failures, MetricFailure{Metric: value.name, Baseline: value.base, Observed: value.current, Threshold: threshold})
		}
	}
	return comparison
}

func (c Comparison) Error() string {
	if len(c.Failures) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Failures))
	for _, failure := range c.Failures {
		parts = append(parts, fmt.Sprintf("%s baseline=%.6f observed=%.6f threshold=%.6f", failure.Metric, failure.Baseline, failure.Observed, failure.Threshold))
	}
	return strings.Join(parts, "; ")
}

func LoadBaseline(path string) (Baseline, error) {
	var b Baseline
	if err := loadJSON(path, &b); err != nil {
		return Baseline{}, err
	}
	if b.SchemaVersion != SchemaVersion || b.Dataset.Version == "" || b.Dataset.CorpusDigest == "" || b.Dataset.CasesDigest == "" {
		return Baseline{}, fmt.Errorf("baseline: require schema version, dataset version, and dataset digests")
	}
	return b, nil
}
func LoadTolerances(path string) (Tolerances, error) {
	var t Tolerances
	if err := loadJSON(path, &t); err != nil {
		return Tolerances{}, err
	}
	if t.SchemaVersion != SchemaVersion || len(t.MaxDrop) == 0 {
		return Tolerances{}, fmt.Errorf("tolerances: require schema version and at least one max_drop value")
	}
	for metric, drop := range t.MaxDrop {
		if drop < 0 {
			return Tolerances{}, fmt.Errorf("tolerances: max_drop for %q cannot be negative", metric)
		}
	}
	return t, nil
}
func loadJSON(path string, target any) error {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return decodeStrict(bytes, target)
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// WriteRun emits an indented, deterministic JSON artifact.
func WriteRun(path string, run Run) error { return writeJSON(path, run) }
func WriteBaseline(path string, run Run) error {
	return writeJSON(path, Baseline{SchemaVersion: SchemaVersion, Dataset: run.Dataset, Configuration: run.Configuration, Metrics: run.Metrics})
}
func writeJSON(path string, value any) error {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(bytes, '\n'), 0o644)
}

func cloneConfig(in *config.Config) *config.Config { out := *in; return &out }

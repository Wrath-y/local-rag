package store

import (
	"math"
	"sort"
)

// RetrieveOpts controls the hybrid retrieval behaviour.
type RetrieveOpts struct {
	TopK                int
	CandidateMultiplier int     // How many candidates to pull from each path (topK * this)
	VectorWeight        float64 // α for score fusion
	BM25Weight          float64 // 1-α
}

// RetrieveResult holds one ranked result from the hybrid search.
type RetrieveResult struct {
	ID         int64
	Text       string
	Source     string
	ParentText string
	ParentID   string
	VecScore   float64
	BM25Score  float64
	FinalScore float64
}

// Retrieve performs hybrid retrieval: vector KNN + FTS5 BM25, merged via score fusion.
func (s *Store) Retrieve(queryVec []float32, queryText string, opts RetrieveOpts) ([]RetrieveResult, error) {
	limit := opts.TopK * opts.CandidateMultiplier
	if limit <= 0 {
		limit = opts.TopK
	}

	// --- 1. Vector path: KNN over vec_chunks ---
	type vecCandidate struct {
		chunkID  int64
		distance float64
	}
	var vecCandidates []vecCandidate

	vecRows, err := s.db.Query(
		`SELECT chunk_id, distance FROM vec_chunks WHERE embedding MATCH ? ORDER BY distance LIMIT ?`,
		Float32ToBytes(queryVec), limit,
	)
	if err == nil {
		defer vecRows.Close()
		for vecRows.Next() {
			var c vecCandidate
			if err := vecRows.Scan(&c.chunkID, &c.distance); err == nil {
				vecCandidates = append(vecCandidates, c)
			}
		}
		vecRows.Close()
	}
	// If vector query errors (e.g. empty table), we simply have no vec candidates.

	// --- 2. BM25 path: FTS5 MATCH ---
	type bm25Candidate struct {
		rowid int64
		rank  float64 // FTS5 rank is negative; more negative = better
	}
	var bm25Candidates []bm25Candidate

	bm25Rows, bm25Err := s.db.Query(
		`SELECT rowid, rank FROM chunks_fts WHERE chunks_fts MATCH ? ORDER BY rank LIMIT ?`,
		queryText, limit,
	)
	if bm25Err == nil {
		defer bm25Rows.Close()
		for bm25Rows.Next() {
			var c bm25Candidate
			if err := bm25Rows.Scan(&c.rowid, &c.rank); err == nil {
				bm25Candidates = append(bm25Candidates, c)
			}
		}
		bm25Rows.Close()
	}
	// If MATCH fails (special chars, empty table), skip BM25 path silently.

	// --- 3. Build candidate map keyed by chunk ID ---
	type candidate struct {
		id         int64
		vecScore   float64 // converted similarity (0..1)
		bm25Score  float64 // normalised (0..1)
		finalScore float64
	}
	cands := make(map[int64]*candidate)

	// Convert vec distance → similarity: sim = 1 / (1 + distance)
	for _, vc := range vecCandidates {
		sim := 1.0 / (1.0 + vc.distance)
		cands[vc.chunkID] = &candidate{id: vc.chunkID, vecScore: sim}
	}

	// Normalise BM25 ranks: find max |rank|, then each normalised = |rank| / maxAbs
	if len(bm25Candidates) > 0 {
		maxAbs := 0.0
		for _, bc := range bm25Candidates {
			if a := math.Abs(bc.rank); a > maxAbs {
				maxAbs = a
			}
		}
		for _, bc := range bm25Candidates {
			var norm float64
			if maxAbs > 0 {
				norm = math.Abs(bc.rank) / maxAbs
			}
			if c, ok := cands[bc.rowid]; ok {
				c.bm25Score = norm
			} else {
				cands[bc.rowid] = &candidate{id: bc.rowid, bm25Score: norm}
			}
		}
	}

	if len(cands) == 0 {
		return nil, nil
	}

	// --- 4. Compute final score and sort ---
	ranked := make([]*candidate, 0, len(cands))
	for _, c := range cands {
		c.finalScore = opts.VectorWeight*c.vecScore + opts.BM25Weight*c.bm25Score
		ranked = append(ranked, c)
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].finalScore > ranked[j].finalScore
	})
	if len(ranked) > opts.TopK {
		ranked = ranked[:opts.TopK]
	}

	// --- 5. Hydrate: fetch text/source/parent from chunks table ---
	results := make([]RetrieveResult, 0, len(ranked))
	for _, c := range ranked {
		var r RetrieveResult
		r.ID = c.id
		r.VecScore = c.vecScore
		r.BM25Score = c.bm25Score
		r.FinalScore = c.finalScore

		err := s.db.QueryRow(
			`SELECT text, source, COALESCE(parent_text, ''), COALESCE(parent_id, '') FROM chunks WHERE id = ?`,
			c.id,
		).Scan(&r.Text, &r.Source, &r.ParentText, &r.ParentID)
		if err != nil {
			// Chunk may have been deleted between query and hydration; skip it.
			continue
		}
		results = append(results, r)
	}

	return results, nil
}

package store

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
)

const FeedbackSchemaVersion = "v1"

// ValidationError is safe to return to local API callers. It contains no
// query, note, or other submitted content.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }
func invalid(message string) error       { return &ValidationError{Message: message} }
func IsValidationError(err error) bool   { var target *ValidationError; return errors.As(err, &target) }

type RetrievalCitation struct {
	CitationID  string  `json:"citation_id"`
	RetrievalID string  `json:"retrieval_id"`
	Rank        int     `json:"rank"`
	ChunkID     int64   `json:"chunk_id"`
	Source      string  `json:"source"`
	ParentID    string  `json:"parent_id,omitempty"`
	VectorScore float64 `json:"vector_score,omitempty"`
	BM25Score   float64 `json:"bm25_score,omitempty"`
	FinalScore  float64 `json:"final_score,omitempty"`
}

type RetrievalEvent struct {
	RetrievalID      string              `json:"retrieval_id"`
	CreatedAt        time.Time           `json:"created_at"`
	Channel          string              `json:"channel"`
	SessionID        string              `json:"session_id,omitempty"`
	QueryFingerprint string              `json:"query_fingerprint"`
	QueryExcerpt     string              `json:"query_excerpt,omitempty"`
	Citations        []RetrievalCitation `json:"citations"`
}

type RecordRetrievalInput struct {
	Query        string
	Channel      string
	SessionID    string
	StoreExcerpt bool
	ExcerptLimit int
	Citations    []RetrievalCitation
	ConfigJSON   string
}

type FeedbackInput struct {
	RetrievalID          string   `json:"retrieval_id"`
	Kind                 string   `json:"kind"`
	SessionID            string   `json:"session_id,omitempty"`
	Note                 string   `json:"note,omitempty"`
	CitationIDs          []string `json:"citation_ids,omitempty"`
	SupersedesFeedbackID string   `json:"supersedes_feedback_id,omitempty"`
}

type FeedbackRecord struct {
	FeedbackID           string    `json:"feedback_id"`
	RetrievalID          string    `json:"retrieval_id"`
	Kind                 string    `json:"kind"`
	SessionID            string    `json:"session_id,omitempty"`
	CitationIDs          []string  `json:"citation_ids,omitempty"`
	SupersedesFeedbackID string    `json:"supersedes_feedback_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	Note                 string    `json:"note,omitempty"`
	Channel              string    `json:"channel,omitempty"`
	QueryExcerpt         string    `json:"query_excerpt,omitempty"`
}

type FeedbackListFilter struct {
	Kind, Channel, RetrievalID, SessionID, Source, CitationID, CandidateStatus string
	From, To                                                                   time.Time
	Cursor                                                                     string
	Limit                                                                      int
	IncludeSuperseded                                                          bool
	IncludeNotes                                                               bool
	IncludeQueryExcerpt                                                        bool
}
type FeedbackPage struct {
	SchemaVersion string           `json:"schema_version"`
	Records       []FeedbackRecord `json:"records"`
	NextCursor    string           `json:"next_cursor,omitempty"`
}
type AggregateRow struct {
	Values map[string]string `json:"values"`
	Count  int               `json:"count"`
}
type AggregateResult struct {
	SchemaVersion string         `json:"schema_version"`
	GroupBy       []string       `json:"group_by"`
	Rows          []AggregateRow `json:"rows"`
}

type Candidate struct {
	CandidateID      string    `json:"candidate_id"`
	FeedbackID       string    `json:"feedback_id"`
	RetrievalID      string    `json:"retrieval_id"`
	CitationID       string    `json:"citation_id,omitempty"`
	Kind             string    `json:"kind"`
	QueryFingerprint string    `json:"query_fingerprint"`
	SchemaVersion    string    `json:"schema_version"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	ReviewNote       string    `json:"review_note,omitempty"`
}

func ensureFeedbackSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS retrieval_events (
  id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, channel TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '', query_fingerprint TEXT NOT NULL,
  query_excerpt TEXT NOT NULL DEFAULT '', config_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS retrieval_citations (
  id TEXT PRIMARY KEY, retrieval_id TEXT NOT NULL REFERENCES retrieval_events(id) ON DELETE CASCADE,
  rank INTEGER NOT NULL, chunk_id INTEGER NOT NULL, source TEXT NOT NULL, parent_id TEXT NOT NULL DEFAULT '',
  vector_score REAL NOT NULL DEFAULT 0, bm25_score REAL NOT NULL DEFAULT 0, final_score REAL NOT NULL DEFAULT 0,
  UNIQUE(retrieval_id, rank)
);
CREATE TABLE IF NOT EXISTS retrieval_feedback (
  id TEXT PRIMARY KEY, retrieval_id TEXT NOT NULL REFERENCES retrieval_events(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN ('helpful','unhelpful','citation-error')),
  session_id TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
  supersedes_feedback_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS feedback_citation_links (
  feedback_id TEXT NOT NULL REFERENCES retrieval_feedback(id) ON DELETE CASCADE,
  citation_id TEXT NOT NULL REFERENCES retrieval_citations(id) ON DELETE CASCADE,
  PRIMARY KEY(feedback_id, citation_id)
);
CREATE TABLE IF NOT EXISTS eval_candidates (
  id TEXT PRIMARY KEY, feedback_id TEXT NOT NULL REFERENCES retrieval_feedback(id) ON DELETE CASCADE,
  retrieval_id TEXT NOT NULL REFERENCES retrieval_events(id) ON DELETE CASCADE,
  citation_id TEXT NOT NULL DEFAULT '', uniqueness_key TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL, query_fingerprint TEXT NOT NULL, query_excerpt TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','rejected')),
  review_note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, reviewed_at INTEGER NOT NULL DEFAULT 0,
  schema_version TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS retrieval_events_created_idx ON retrieval_events(created_at, id);
CREATE INDEX IF NOT EXISTS retrieval_events_session_idx ON retrieval_events(session_id);
CREATE INDEX IF NOT EXISTS retrieval_citations_source_idx ON retrieval_citations(source, retrieval_id);
CREATE INDEX IF NOT EXISTS retrieval_feedback_created_idx ON retrieval_feedback(created_at, id);
CREATE INDEX IF NOT EXISTS retrieval_feedback_kind_idx ON retrieval_feedback(kind, retrieval_id);
CREATE INDEX IF NOT EXISTS eval_candidates_status_idx ON eval_candidates(status, created_at, id);`)
	return err
}

func loadOrCreateFeedbackKey(dbPath string) ([]byte, error) {
	path := dbPath + ".feedback-hmac.key"
	if key, err := os.ReadFile(path); err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("feedback key has invalid length")
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read feedback key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate feedback key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create feedback key directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return os.ReadFile(path)
		}
		return nil, fmt.Errorf("create feedback key: %w", err)
	}
	_, writeErr := f.Write(key)
	closeErr := f.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return key, nil
}

func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(norm.NFC.String(query)), " ")
}
func (s *Store) QueryFingerprint(query string) (string, error) {
	if len(s.feedbackKey) != 32 {
		return "", fmt.Errorf("feedback fingerprint key unavailable")
	}
	m := hmac.New(sha256.New, s.feedbackKey)
	_, _ = m.Write([]byte(normalizeQuery(query)))
	return hex.EncodeToString(m.Sum(nil)), nil
}
func RedactQueryExcerpt(query string, limit int) string {
	text := norm.NFC.String(query)
	for _, token := range strings.Fields(text) {
		if strings.Contains(token, "=") && (strings.Contains(strings.ToLower(token), "key=") || strings.Contains(strings.ToLower(token), "token=") || strings.Contains(strings.ToLower(token), "secret=") || strings.Contains(strings.ToLower(token), "password=")) {
			text = strings.ReplaceAll(text, token, "[REDACTED]")
		}
	}
	for _, part := range strings.Fields(text) {
		if u, err := url.Parse(part); err == nil && (u.RawQuery != "" || u.Fragment != "") {
			u.RawQuery = ""
			u.Fragment = ""
			text = strings.ReplaceAll(text, part, u.String())
		}
	}
	if limit < 1 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
func ValidNote(note string, max int) (string, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > max {
		return "", invalid("note exceeds configured length")
	}
	for _, r := range note {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", invalid("note contains control characters")
		}
	}
	return note, nil
}

func (s *Store) RecordRetrieval(input RecordRetrievalInput) (RetrievalEvent, error) {
	if strings.TrimSpace(input.Channel) == "" {
		return RetrievalEvent{}, invalid("retrieval channel is required")
	}
	fingerprint, err := s.QueryFingerprint(input.Query)
	if err != nil {
		return RetrievalEvent{}, err
	}
	if input.SessionID != "" {
		ok, err := s.sessionExists(input.SessionID)
		if err != nil {
			return RetrievalEvent{}, err
		}
		if !ok {
			return RetrievalEvent{}, invalid("unknown session_id")
		}
	}
	event := RetrievalEvent{RetrievalID: uuid.NewString(), CreatedAt: time.Now().UTC(), Channel: input.Channel, SessionID: input.SessionID, QueryFingerprint: fingerprint}
	if input.StoreExcerpt {
		event.QueryExcerpt = RedactQueryExcerpt(input.Query, input.ExcerptLimit)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO retrieval_events(id,created_at,channel,session_id,query_fingerprint,query_excerpt,config_json) VALUES(?,?,?,?,?,?,?)`, event.RetrievalID, event.CreatedAt.Unix(), event.Channel, event.SessionID, fingerprint, event.QueryExcerpt, defaultJSON(input.ConfigJSON)); err != nil {
		return event, err
	}
	for index, citation := range input.Citations {
		citation.CitationID = uuid.NewString()
		citation.RetrievalID = event.RetrievalID
		citation.Rank = index + 1
		event.Citations = append(event.Citations, citation)
		if _, err = tx.Exec(`INSERT INTO retrieval_citations(id,retrieval_id,rank,chunk_id,source,parent_id,vector_score,bm25_score,final_score) VALUES(?,?,?,?,?,?,?,?,?)`, citation.CitationID, citation.RetrievalID, citation.Rank, citation.ChunkID, citation.Source, citation.ParentID, citation.VectorScore, citation.BM25Score, citation.FinalScore); err != nil {
			return event, err
		}
	}
	if err = tx.Commit(); err != nil {
		return event, err
	}
	return event, nil
}
func defaultJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}
func (s *Store) sessionExists(id string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='sessions')`).Scan(&exists)
	if err != nil {
		return false, err
	}
	if exists == 0 {
		return false, nil
	}
	err = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id=?)`, id).Scan(&exists)
	return exists == 1, err
}

func (s *Store) CreateFeedback(input FeedbackInput, noteMax, retentionDays int) (FeedbackRecord, error) {
	if input.Kind != "helpful" && input.Kind != "unhelpful" && input.Kind != "citation-error" {
		return FeedbackRecord{}, invalid("kind must be helpful, unhelpful, or citation-error")
	}
	if input.RetrievalID == "" {
		return FeedbackRecord{}, invalid("retrieval_id is required")
	}
	if input.Kind == "citation-error" && len(input.CitationIDs) == 0 {
		return FeedbackRecord{}, invalid("citation-error requires citation_ids")
	}
	note, err := ValidNote(input.Note, noteMax)
	if err != nil {
		return FeedbackRecord{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return FeedbackRecord{}, err
	}
	defer tx.Rollback()
	if err = cleanupFeedbackTx(tx, retentionDays); err != nil {
		return FeedbackRecord{}, err
	}
	var eventSession string
	err = tx.QueryRow(`SELECT session_id FROM retrieval_events WHERE id=?`, input.RetrievalID).Scan(&eventSession)
	if errors.Is(err, sql.ErrNoRows) {
		return FeedbackRecord{}, invalid("unknown or expired retrieval_id")
	}
	if err != nil {
		return FeedbackRecord{}, err
	}
	if input.SessionID != "" {
		ok, err := s.sessionExists(input.SessionID)
		if err != nil {
			return FeedbackRecord{}, err
		}
		if !ok {
			return FeedbackRecord{}, invalid("unknown session_id")
		}
		if eventSession != "" && eventSession != input.SessionID {
			return FeedbackRecord{}, invalid("session_id does not match retrieval")
		}
	}
	if input.SupersedesFeedbackID != "" {
		var priorRetrieval string
		err = tx.QueryRow(`SELECT retrieval_id FROM retrieval_feedback WHERE id=?`, input.SupersedesFeedbackID).Scan(&priorRetrieval)
		if errors.Is(err, sql.ErrNoRows) {
			return FeedbackRecord{}, invalid("unknown supersedes_feedback_id")
		}
		if err != nil {
			return FeedbackRecord{}, err
		}
		if priorRetrieval != input.RetrievalID {
			return FeedbackRecord{}, invalid("superseded feedback belongs to another retrieval")
		}
	}
	unique := map[string]bool{}
	for _, citationID := range input.CitationIDs {
		if citationID == "" || unique[citationID] {
			return FeedbackRecord{}, invalid("citation_ids must be unique non-empty values")
		}
		unique[citationID] = true
		var exists int
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM retrieval_citations WHERE id=? AND retrieval_id=?)`, citationID, input.RetrievalID).Scan(&exists)
		if err != nil {
			return FeedbackRecord{}, err
		}
		if exists == 0 {
			return FeedbackRecord{}, invalid("citation_id does not belong to retrieval")
		}
	}
	record := FeedbackRecord{FeedbackID: uuid.NewString(), RetrievalID: input.RetrievalID, Kind: input.Kind, SessionID: input.SessionID, CitationIDs: append([]string(nil), input.CitationIDs...), SupersedesFeedbackID: input.SupersedesFeedbackID, CreatedAt: time.Now().UTC(), Note: note}
	if _, err = tx.Exec(`INSERT INTO retrieval_feedback(id,retrieval_id,kind,session_id,note,supersedes_feedback_id,created_at) VALUES(?,?,?,?,?,?,?)`, record.FeedbackID, record.RetrievalID, record.Kind, record.SessionID, record.Note, record.SupersedesFeedbackID, record.CreatedAt.Unix()); err != nil {
		return FeedbackRecord{}, err
	}
	for _, id := range record.CitationIDs {
		if _, err = tx.Exec(`INSERT INTO feedback_citation_links(feedback_id,citation_id) VALUES(?,?)`, record.FeedbackID, id); err != nil {
			return FeedbackRecord{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return FeedbackRecord{}, err
	}
	return record, nil
}

func cleanupFeedbackTx(tx *sql.Tx, retentionDays int) error {
	if retentionDays < 1 {
		return invalid("retention_days must be positive")
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	if _, err := tx.Exec(`DELETE FROM retrieval_feedback WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM retrieval_events WHERE created_at < ? AND NOT EXISTS(SELECT 1 FROM retrieval_feedback f WHERE f.retrieval_id=retrieval_events.id)`, cutoff)
	return err
}
func (s *Store) CleanupFeedback(retentionDays int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = cleanupFeedbackTx(tx, retentionDays); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListFeedback(filter FeedbackListFilter, retentionDays int) (FeedbackPage, error) {
	if err := s.CleanupFeedback(retentionDays); err != nil {
		return FeedbackPage{}, err
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 10000 {
		return FeedbackPage{}, invalid("limit exceeds 10000")
	}
	where, args := feedbackWhere(filter)
	query := `SELECT f.id,f.retrieval_id,f.kind,f.session_id,f.note,f.supersedes_feedback_id,f.created_at,e.channel,e.query_excerpt FROM retrieval_feedback f JOIN retrieval_events e ON e.id=f.retrieval_id ` + where + ` ORDER BY f.created_at ASC,f.id ASC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return FeedbackPage{}, err
	}
	defer rows.Close()
	page := FeedbackPage{SchemaVersion: FeedbackSchemaVersion}
	for rows.Next() {
		var r FeedbackRecord
		var at int64
		if err = rows.Scan(&r.FeedbackID, &r.RetrievalID, &r.Kind, &r.SessionID, &r.Note, &r.SupersedesFeedbackID, &at, &r.Channel, &r.QueryExcerpt); err != nil {
			return page, err
		}
		r.CreatedAt = time.Unix(at, 0).UTC()
		ids, err := s.feedbackCitationIDs(r.FeedbackID)
		if err != nil {
			return page, err
		}
		r.CitationIDs = ids
		if !filter.IncludeNotes {
			r.Note = ""
		}
		if !filter.IncludeQueryExcerpt {
			r.QueryExcerpt = ""
		}
		page.Records = append(page.Records, r)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Records) > filter.Limit {
		last := page.Records[filter.Limit-1]
		page.NextCursor = fmt.Sprintf("%d|%s", last.CreatedAt.Unix(), last.FeedbackID)
		page.Records = page.Records[:filter.Limit]
	}
	return page, nil
}
func feedbackWhere(f FeedbackListFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	add := func(condition string, v any) { clauses = append(clauses, condition); args = append(args, v) }
	if f.Kind != "" {
		add("f.kind=?", f.Kind)
	}
	if f.Channel != "" {
		add("e.channel=?", f.Channel)
	}
	if f.RetrievalID != "" {
		add("f.retrieval_id=?", f.RetrievalID)
	}
	if f.SessionID != "" {
		add("f.session_id=?", f.SessionID)
	}
	if !f.From.IsZero() {
		add("f.created_at>=?", f.From.Unix())
	}
	if !f.To.IsZero() {
		add("f.created_at<=?", f.To.Unix())
	}
	if f.Source != "" {
		add("EXISTS(SELECT 1 FROM feedback_citation_links l JOIN retrieval_citations c ON c.id=l.citation_id WHERE l.feedback_id=f.id AND c.source=?)", f.Source)
	}
	if f.CitationID != "" {
		add("EXISTS(SELECT 1 FROM feedback_citation_links l WHERE l.feedback_id=f.id AND l.citation_id=?)", f.CitationID)
	}
	if f.CandidateStatus != "" {
		add("EXISTS(SELECT 1 FROM eval_candidates ec WHERE ec.feedback_id=f.id AND ec.status=?)", f.CandidateStatus)
	}
	if !f.IncludeSuperseded {
		clauses = append(clauses, "NOT EXISTS(SELECT 1 FROM retrieval_feedback newer WHERE newer.supersedes_feedback_id=f.id)")
	}
	if f.Cursor != "" {
		parts := strings.SplitN(f.Cursor, "|", 2)
		if len(parts) == 2 {
			var ts int64
			if _, err := fmt.Sscan(parts[0], &ts); err == nil {
				clauses = append(clauses, "(f.created_at>? OR (f.created_at=? AND f.id>?))")
				args = append(args, ts, ts, parts[1])
			}
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
func (s *Store) feedbackCitationIDs(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT citation_id FROM feedback_citation_links WHERE feedback_id=? ORDER BY citation_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		ids = append(ids, v)
	}
	return ids, rows.Err()
}

func (s *Store) AggregateFeedback(filter FeedbackListFilter, groupBy []string, retentionDays int) (AggregateResult, error) {
	allowed := map[string]string{"kind": "f.kind", "channel": "e.channel", "source": "c.source", "citation_rank": "CAST(c.rank AS TEXT)", "date": "strftime('%Y-%m-%d',f.created_at,'unixepoch')"}
	if len(groupBy) == 0 {
		return AggregateResult{}, invalid("at least one grouping dimension is required")
	}
	cols := make([]string, 0, len(groupBy))
	for _, g := range groupBy {
		v, ok := allowed[g]
		if !ok {
			return AggregateResult{}, invalid("unsupported aggregation dimension")
		}
		cols = append(cols, v+" AS "+g)
	}
	if err := s.CleanupFeedback(retentionDays); err != nil {
		return AggregateResult{}, err
	}
	where, args := feedbackWhere(filter)
	query := `SELECT ` + strings.Join(cols, ",") + `,COUNT(*) FROM retrieval_feedback f JOIN retrieval_events e ON e.id=f.retrieval_id LEFT JOIN feedback_citation_links l ON l.feedback_id=f.id LEFT JOIN retrieval_citations c ON c.id=l.citation_id ` + where + ` GROUP BY ` + strings.Join(groupBy, ",") + ` ORDER BY ` + strings.Join(groupBy, ",")
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return AggregateResult{}, err
	}
	defer rows.Close()
	out := AggregateResult{SchemaVersion: FeedbackSchemaVersion, GroupBy: groupBy}
	for rows.Next() {
		values := map[string]string{}
		dest := make([]any, len(groupBy)+1)
		holders := make([]sql.NullString, len(groupBy))
		for i := range holders {
			dest[i] = &holders[i]
		}
		var count int
		dest[len(groupBy)] = &count
		if err = rows.Scan(dest...); err != nil {
			return out, err
		}
		for i, g := range groupBy {
			values[g] = holders[i].String
		}
		out.Rows = append(out.Rows, AggregateRow{Values: values, Count: count})
	}
	return out, rows.Err()
}

func (s *Store) ExportFeedback(filter FeedbackListFilter, format string, includeNotes, includeExcerpt bool, max, retentionDays int) ([]byte, error) {
	if max < 1 {
		return nil, invalid("export maximum must be positive")
	}
	filter.Limit = max + 1
	filter.IncludeNotes = includeNotes
	filter.IncludeQueryExcerpt = includeExcerpt
	page, err := s.ListFeedback(filter, retentionDays)
	if err != nil {
		return nil, err
	}
	if len(page.Records) > max {
		return nil, invalid("export exceeds configured maximum")
	}
	if format == "jsonl" {
		var b strings.Builder
		meta, _ := json.Marshal(map[string]any{"schema_version": FeedbackSchemaVersion, "filters": filter, "notes_included": includeNotes, "query_excerpts_included": includeExcerpt})
		b.Write(meta)
		b.WriteByte('\n')
		for _, r := range page.Records {
			line, _ := json.Marshal(r)
			b.Write(line)
			b.WriteByte('\n')
		}
		return []byte(b.String()), nil
	}
	if format != "csv" {
		return nil, invalid("format must be jsonl or csv")
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"schema_version", "feedback_id", "retrieval_id", "kind", "session_id", "citation_ids", "created_at", "channel", "note", "query_excerpt"})
	for _, r := range page.Records {
		_ = w.Write([]string{FeedbackSchemaVersion, r.FeedbackID, r.RetrievalID, r.Kind, r.SessionID, strings.Join(r.CitationIDs, ";"), r.CreatedAt.Format(time.RFC3339), r.Channel, r.Note, r.QueryExcerpt})
	}
	w.Flush()
	return []byte(b.String()), w.Error()
}

func (s *Store) ConvertCandidates(filter FeedbackListFilter, includeExcerpt, includeNote bool, max, retentionDays int) ([]Candidate, int, error) {
	if max < 1 {
		return nil, 0, invalid("candidate conversion maximum must be positive")
	}
	filter.Limit = max
	page, err := s.ListFeedback(filter, retentionDays)
	if err != nil {
		return nil, 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var created []Candidate
	skipped := 0
	for _, f := range page.Records {
		scopes := f.CitationIDs
		if len(scopes) == 0 {
			scopes = []string{""}
		}
		for _, citationID := range scopes {
			key := f.FeedbackID + ":" + citationID
			candidate := Candidate{CandidateID: uuid.NewString(), FeedbackID: f.FeedbackID, RetrievalID: f.RetrievalID, CitationID: citationID, Kind: f.Kind, Status: "pending", CreatedAt: time.Now().UTC(), SchemaVersion: FeedbackSchemaVersion}
			var fingerprint, excerpt string
			err = tx.QueryRow(`SELECT query_fingerprint,query_excerpt FROM retrieval_events WHERE id=?`, f.RetrievalID).Scan(&fingerprint, &excerpt)
			if err != nil {
				return nil, 0, err
			}
			if !includeExcerpt {
				excerpt = ""
			}
			candidate.QueryFingerprint = fingerprint
			note := f.Note
			if !includeNote {
				note = ""
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO eval_candidates(id,feedback_id,retrieval_id,citation_id,uniqueness_key,kind,query_fingerprint,query_excerpt,note,status,created_at,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, candidate.CandidateID, candidate.FeedbackID, candidate.RetrievalID, candidate.CitationID, key, candidate.Kind, fingerprint, excerpt, note, candidate.Status, candidate.CreatedAt.Unix(), candidate.SchemaVersion)
			if err != nil {
				return nil, 0, err
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				skipped++
			} else {
				created = append(created, candidate)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return created, skipped, nil
}
func (s *Store) ListCandidates(status, kind, source string, limit int) ([]Candidate, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id,feedback_id,retrieval_id,citation_id,kind,query_fingerprint,schema_version,status,created_at,review_note FROM eval_candidates WHERE 1=1`
	args := []any{}
	if status != "" {
		q += " AND status=?"
		args = append(args, status)
	}
	if kind != "" {
		q += " AND kind=?"
		args = append(args, kind)
	}
	if source != "" {
		q += " AND citation_id IN (SELECT id FROM retrieval_citations WHERE source=?)"
		args = append(args, source)
	}
	q += " ORDER BY created_at,id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		var at int64
		if err = rows.Scan(&c.CandidateID, &c.FeedbackID, &c.RetrievalID, &c.CitationID, &c.Kind, &c.QueryFingerprint, &c.SchemaVersion, &c.Status, &at, &c.ReviewNote); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(at, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) ReviewCandidate(id, status, note string, maxNote int) (Candidate, error) {
	if status != "approved" && status != "rejected" {
		return Candidate{}, invalid("status must be approved or rejected")
	}
	note, err := ValidNote(note, maxNote)
	if err != nil {
		return Candidate{}, err
	}
	res, err := s.db.Exec(`UPDATE eval_candidates SET status=?,review_note=?,reviewed_at=? WHERE id=? AND status='pending'`, status, note, time.Now().Unix(), id)
	if err != nil {
		return Candidate{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Candidate{}, invalid("candidate is unknown or already reviewed")
	}
	var candidate Candidate
	var createdAt int64
	err = s.db.QueryRow(`SELECT id,feedback_id,retrieval_id,citation_id,kind,query_fingerprint,schema_version,status,created_at,review_note FROM eval_candidates WHERE id=?`, id).Scan(&candidate.CandidateID, &candidate.FeedbackID, &candidate.RetrievalID, &candidate.CitationID, &candidate.Kind, &candidate.QueryFingerprint, &candidate.SchemaVersion, &candidate.Status, &createdAt, &candidate.ReviewNote)
	if err != nil {
		return Candidate{}, err
	}
	candidate.CreatedAt = time.Unix(createdAt, 0).UTC()
	return candidate, nil
}

// Stable helper used by tests and callers that need deterministic input order.
func SortedIDs(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

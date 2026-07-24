package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/store"
)

func (h *Handler) feedbackUnavailable(c *gin.Context) bool {
	if h.deps.Config == nil || !h.deps.Config.Feedback.Enabled || h.deps.Stores == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "feedback capture is disabled"})
		return true
	}
	return false
}

func strictJSON(c *gin.Context, target any) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return &store.ValidationError{Message: "request must contain one JSON object"}
	}
	return nil
}
func feedbackError(c *gin.Context, err error) {
	observe.FeedbackOperations.WithLabelValues("validation_rejected").Inc()
	if store.IsValidationError(err) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "feedback operation failed"})
}

// CreateFeedback records one immutable disposition for a previous retrieval.
func (h *Handler) CreateFeedback(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	var input store.FeedbackInput
	if err := strictJSON(c, &input); err != nil {
		feedbackError(c, err)
		return
	}
	var record store.FeedbackRecord
	err := h.deps.Stores.WithWriteStore(func(st *store.Store) error {
		var err error
		record, err = st.CreateFeedback(input, h.deps.Config.Feedback.NoteMaxChars, h.deps.Config.Feedback.RetentionDays)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	observe.FeedbackOperations.WithLabelValues("capture_ok").Inc()
	c.JSON(http.StatusCreated, gin.H{"schema_version": store.FeedbackSchemaVersion, "feedback": record})
}

func feedbackFilter(c *gin.Context) (store.FeedbackListFilter, error) {
	f := store.FeedbackListFilter{Kind: c.Query("kind"), Channel: c.Query("channel"), RetrievalID: c.Query("retrieval_id"), SessionID: c.Query("session_id"), Source: c.Query("source"), CitationID: c.Query("citation_id"), CandidateStatus: c.Query("candidate_status"), Cursor: c.Query("cursor"), IncludeSuperseded: c.Query("include_superseded") == "true", IncludeNotes: c.Query("include_notes") == "true", IncludeQueryExcerpt: c.Query("include_query_excerpt") == "true"}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return f, &store.ValidationError{Message: "invalid limit"}
		}
		f.Limit = n
	}
	parseTime := func(raw string) (time.Time, error) {
		if raw == "" {
			return time.Time{}, nil
		}
		return time.Parse(time.RFC3339, raw)
	}
	var err error
	if f.From, err = parseTime(c.Query("from")); err != nil {
		return f, &store.ValidationError{Message: "from must be RFC3339"}
	}
	if f.To, err = parseTime(c.Query("to")); err != nil {
		return f, &store.ValidationError{Message: "to must be RFC3339"}
	}
	return f, nil
}
func (h *Handler) ListFeedback(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	filter, err := feedbackFilter(c)
	if err != nil {
		feedbackError(c, err)
		return
	}
	var page store.FeedbackPage
	err = h.deps.Stores.WithStore(func(st *store.Store) error {
		var err error
		page, err = st.ListFeedback(filter, h.deps.Config.Feedback.RetentionDays)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	observe.FeedbackOperations.WithLabelValues("list_ok").Inc()
	c.JSON(http.StatusOK, page)
}
func (h *Handler) AggregateFeedback(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	filter, err := feedbackFilter(c)
	if err != nil {
		feedbackError(c, err)
		return
	}
	groups := strings.Split(c.Query("group_by"), ",")
	var result store.AggregateResult
	err = h.deps.Stores.WithStore(func(st *store.Store) error {
		var err error
		result, err = st.AggregateFeedback(filter, groups, h.deps.Config.Feedback.RetentionDays)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	observe.FeedbackOperations.WithLabelValues("aggregate_ok").Inc()
	c.JSON(http.StatusOK, result)
}
func (h *Handler) ExportFeedback(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	filter, err := feedbackFilter(c)
	if err != nil {
		feedbackError(c, err)
		return
	}
	format := c.DefaultQuery("format", "jsonl")
	notes := c.Query("include_notes") == "true"
	excerpts := c.Query("include_query_excerpt") == "true"
	var content []byte
	err = h.deps.Stores.WithStore(func(st *store.Store) error {
		var err error
		content, err = st.ExportFeedback(filter, format, notes, excerpts, h.deps.Config.Feedback.ExportMaxRecords, h.deps.Config.Feedback.RetentionDays)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	observe.FeedbackOperations.WithLabelValues("export_ok").Inc()
	if format == "csv" {
		c.Data(http.StatusOK, "text/csv; charset=utf-8", content)
	} else {
		c.Data(http.StatusOK, "application/x-ndjson", content)
	}
}

type candidateConvertRequest struct {
	Filters             store.FeedbackListFilter `json:"filters"`
	IncludeQueryExcerpt bool                     `json:"include_query_excerpt"`
	IncludeNote         bool                     `json:"include_note"`
}

func (h *Handler) ConvertCandidates(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	var req candidateConvertRequest
	if err := strictJSON(c, &req); err != nil {
		feedbackError(c, err)
		return
	}
	var items []store.Candidate
	var skipped int
	err := h.deps.Stores.WithWriteStore(func(st *store.Store) error {
		var err error
		items, skipped, err = st.ConvertCandidates(req.Filters, req.IncludeQueryExcerpt, req.IncludeNote, h.deps.Config.Feedback.CandidateConversionMax, h.deps.Config.Feedback.RetentionDays)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	observe.FeedbackOperations.WithLabelValues("conversion_ok").Inc()
	c.JSON(http.StatusOK, gin.H{"schema_version": store.FeedbackSchemaVersion, "created": len(items), "skipped": skipped, "candidate_ids": candidateIDs(items)})
}
func candidateIDs(items []store.Candidate) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.CandidateID
	}
	return out
}
func (h *Handler) ListCandidates(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	var items []store.Candidate
	err := h.deps.Stores.WithStore(func(st *store.Store) error {
		var err error
		items, err = st.ListCandidates(c.Query("status"), c.Query("kind"), c.Query("source"), limit)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"schema_version": store.FeedbackSchemaVersion, "candidates": items})
}

// ExportCandidates returns a bounded, privacy-minimized candidate view. Notes
// and opt-in excerpts remain local database fields and are not exported here.
func (h *Handler) ExportCandidates(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	limit := h.deps.Config.Feedback.ExportMaxRecords
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= limit {
			limit = parsed
		} else {
			feedbackError(c, &store.ValidationError{Message: "invalid export limit"})
			return
		}
	}
	var items []store.Candidate
	err := h.deps.Stores.WithStore(func(st *store.Store) error {
		var err error
		items, err = st.ListCandidates(c.Query("status"), c.Query("kind"), c.Query("source"), limit)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	observe.FeedbackOperations.WithLabelValues("candidate_export_ok").Inc()
	c.JSON(http.StatusOK, gin.H{"schema_version": store.FeedbackSchemaVersion, "candidates": items})
}

type candidateReviewRequest struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

func (h *Handler) ReviewCandidate(c *gin.Context) {
	if h.feedbackUnavailable(c) {
		return
	}
	var req candidateReviewRequest
	if err := strictJSON(c, &req); err != nil {
		feedbackError(c, err)
		return
	}
	var item store.Candidate
	err := h.deps.Stores.WithWriteStore(func(st *store.Store) error {
		var err error
		item, err = st.ReviewCandidate(c.Param("id"), req.Status, req.Note, h.deps.Config.Feedback.ReviewNoteMaxChars)
		return err
	})
	if err != nil {
		feedbackError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"candidate": item})
}

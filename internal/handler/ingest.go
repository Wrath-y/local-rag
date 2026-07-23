package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/document"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/store"
)

type ingestRequest struct {
	Text       string             `json:"text"`
	Source     string             `json:"source"`
	Path       string             `json:"path"`
	URL        string             `json:"url"`
	Title      string             `json:"title"`
	URI        string             `json:"uri"`
	Location   string             `json:"location"`
	Kind       document.InputKind `json:"kind"`
	Ref        string             `json:"ref"`
	Exclusions []string           `json:"exclusions"`
	Limits     document.Limits    `json:"limits"`
}

// Ingest accepts legacy text/source requests as well as supported loader
// inputs, then delegates them through the common document ingest service.
func (h *Handler) Ingest(c *gin.Context) {
	var req ingestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeIngestError(c, document.NewError(document.InvalidInput, "Provide a valid ingest request.", err))
		return
	}
	result, err := h.ingestService.Ingest(c.Request.Context(), req.documentRequest())
	if err != nil {
		if category, ok := document.CategoryOf(err); ok {
			observe.IngestTotal.WithLabelValues(string(category)).Inc()
		}
		writeIngestError(c, err)
		return
	}
	if result.ChunksAdded > 0 {
		observe.IngestTotal.WithLabelValues("ok").Inc()
		observe.ChunkTotal.Add(float64(result.ChunksAdded))
		c.JSON(http.StatusOK, gin.H{"status": "ok", "chunks_added": result.ChunksAdded})
		return
	}
	observe.IngestTotal.WithLabelValues("skip").Inc()
	c.JSON(http.StatusOK, gin.H{"status": "skip", "chunks_added": 0})
}

func (req ingestRequest) documentRequest() document.Request {
	request := document.Request{
		Kind: req.Kind, Text: req.Text, Path: req.Path, URL: req.URL, Source: req.Source, Ref: req.Ref, Exclusions: req.Exclusions, Limits: req.Limits,
		Provenance: map[string]string{"title": req.Title, "uri": req.URI, "location": req.Location},
	}
	// Existing text/source callers always select the direct-text loader. Path
	// and URL are opt-in fields, so literal text that resembles a URL remains
	// compatible with the legacy endpoint.
	if request.Kind == document.InputAuto && (req.Text != "" || (req.Path == "" && req.URL == "")) {
		request.Kind = document.InputText
	}
	return request
}

func (h *Handler) ingestDocument(ctx context.Context, documentValue document.Document) (int, error) {
	chunks, err := h.deps.Chunker.Chunk(documentValue.Content, documentValue.Metadata.Source)
	if err != nil {
		return 0, fmt.Errorf("chunk document: %w", err)
	}
	if len(chunks) == 0 {
		return 0, nil
	}

	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.Text
	}
	embeddings, err := h.deps.Embedder.Embed(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("embed document: %w", err)
	}
	if len(embeddings) != len(chunks) {
		return 0, fmt.Errorf("embedding count mismatch")
	}

	added := 0
	err = h.deps.Stores.WithWriteStore(func(st *store.Store) error {
		for i, ch := range chunks {
			uri := documentValue.Metadata.Provenance["uri"]
			if uri == "" {
				uri = ch.URI
			}
			if uri == "" {
				uri = ch.Source
			}
			title := documentValue.Metadata.Provenance["title"]
			if title == "" {
				title = ch.Title
			}
			location := documentValue.Metadata.Provenance["location"]
			if location == "" {
				location = ch.Location
			}
			if location == "" {
				location = fmt.Sprintf("chunk:%d", i+1)
			}
			id, err := st.InsertChunkWithProvenance(
				ch.Text, ch.Source, ch.MD5, ch.ParentText, ch.ParentID,
				store.Provenance{Title: title, URI: uri, Location: location, ConnectorMetadata: document.MetadataJSON(documentValue.Metadata)}, embeddings[i],
			)
			if err != nil {
				return err
			}
			if id != 0 {
				added++
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store document: %w", err)
	}
	return added, nil
}

func writeIngestError(c *gin.Context, err error) {
	category, ok := document.CategoryOf(err)
	if !ok {
		category = document.IngestFailed
	}
	status := http.StatusInternalServerError
	switch category {
	case document.UnsupportedInput, document.InvalidInput, document.PolicyRejected:
		status = http.StatusBadRequest
	case document.ExtractionFailed:
		status = http.StatusUnprocessableEntity
	case document.LimitExceeded:
		status = http.StatusRequestEntityTooLarge
	case document.UnavailableInput:
		status = http.StatusServiceUnavailable
	case document.LoadFailed:
		status = http.StatusBadGateway
	case document.IngestFailed:
		if errors.Is(err, ErrRebuildInProgress) {
			status = http.StatusServiceUnavailable
		}
	}
	// PublicMessage deliberately excludes errors from filesystem, credentials,
	// and external resolver responses.
	c.JSON(status, gin.H{"error": document.PublicMessage(err), "code": string(category)})
}

package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/store"
)

type ingestRequest struct {
	Text     string `json:"text" binding:"required"`
	Source   string `json:"source"`
	Title    string `json:"title"`
	URI      string `json:"uri"`
	Location string `json:"location"`
}

// Ingest accepts text, chunks it, embeds each chunk, and stores it.
func (h *Handler) Ingest(c *gin.Context) {
	var req ingestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}
	if req.Source == "" {
		req.Source = "manual"
	}

	// 1. Chunk the text.
	chunks, err := h.deps.Chunker.Chunk(req.Text, req.Source)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "chunking failed: " + err.Error()})
		return
	}
	if len(chunks) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "skip", "chunks_added": 0})
		return
	}

	// 2. Collect texts for batch embedding.
	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.Text
	}

	// 3. Embed all chunks in one batch.
	embeddings, err := h.deps.Embedder.Embed(context.Background(), texts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "embedding failed: " + err.Error()})
		return
	}
	if len(embeddings) != len(chunks) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "embedding count mismatch"})
		return
	}

	// 4. Store each chunk under the lifecycle read lock.
	added := 0
	err = h.deps.Stores.WithWriteStore(func(st *store.Store) error {
		for i, ch := range chunks {
			uri := req.URI
			if uri == "" {
				uri = ch.URI
			}
			if uri == "" {
				uri = ch.Source
			}
			title := req.Title
			if title == "" {
				title = ch.Title
			}
			location := req.Location
			if location == "" {
				location = ch.Location
			}
			if location == "" {
				location = fmt.Sprintf("chunk:%d", i+1)
			}
			id, err := st.InsertChunkWithProvenance(
				ch.Text, ch.Source, ch.MD5, ch.ParentText, ch.ParentID,
				store.Provenance{Title: title, URI: uri, Location: location}, embeddings[i],
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
		if err == ErrRebuildInProgress {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store failed: " + err.Error()})
		return
	}

	// 5. Update metrics.
	if added > 0 {
		observe.IngestTotal.WithLabelValues("ok").Inc()
		observe.ChunkTotal.Add(float64(added))
	} else {
		observe.IngestTotal.WithLabelValues("skip").Inc()
	}

	if added == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "skip", "chunks_added": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "chunks_added": added})
}

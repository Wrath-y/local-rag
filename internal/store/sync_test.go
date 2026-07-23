package store

import "testing"

func testSnapshot(source string, docs ...SyncDocument) SyncSnapshot {
	return SyncSnapshot{Source: source, Documents: docs, Identity: SyncIdentity{Canonicalization: "utf8-trim-crlf-v1", Chunker: "test-v1", EmbeddingModel: "test-model"}}
}

func testDocument(id string, chunks ...SyncChunk) SyncDocument {
	content := "document " + id
	for _, c := range chunks {
		content += c.Content
	}
	return SyncDocument{ID: id, Content: content, Chunks: chunks}
}

func runSync(t *testing.T, st *Store, snapshot SyncSnapshot) (SyncTask, SyncReport) {
	t.Helper()
	task, _, err := st.SubmitSync(snapshot, "", 0)
	if err != nil {
		t.Fatalf("SubmitSync: %v", err)
	}
	claimed, prepared, ok, err := st.ClaimNextSyncTask()
	if err != nil || !ok {
		t.Fatalf("ClaimNextSyncTask = %v, %v", ok, err)
	}
	diff, err := st.PrepareSyncDiff(prepared)
	if err != nil {
		t.Fatalf("PrepareSyncDiff: %v", err)
	}
	for _, d := range prepared.Documents {
		for _, c := range d.Chunks {
			if diff.EmbedKeys[ChunkKey(prepared.Source, d.ID, c.Key)] {
				if err := st.StageSyncEmbedding(claimed.ID, prepared.Source, d.ID, c, []float32{1, 2}); err != nil {
					t.Fatalf("StageSyncEmbedding: %v", err)
				}
			}
		}
	}
	report, err := st.PromoteSync(claimed, prepared, diff)
	if err != nil {
		t.Fatalf("PromoteSync: %v", err)
	}
	return task, report
}

func TestIncrementalSyncPromotesAndReusesChunks(t *testing.T) {
	st, err := New(t.TempDir()+"/sync.db", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertChunk("legacy", "source-a", "legacy-md5", "", "", []float32{1, 2}); err != nil {
		t.Fatal(err)
	}
	first := testSnapshot("source-a", testDocument("doc-1", SyncChunk{Key: "a", Content: "alpha"}, SyncChunk{Key: "b", Content: "beta"}))
	_, report := runSync(t, st, first)
	if report.ResultRevision != 1 || report.EmbeddedChunks != 2 {
		t.Fatalf("first report = %#v", report)
	}
	if count, _ := st.ChunkCount(); count != 2 {
		t.Fatalf("chunks after first sync = %d", count)
	}
	_, report = runSync(t, st, first)
	if report.ReusedChunks != 2 || report.EmbeddedChunks != 0 || report.Chunks.Unchanged != 2 {
		t.Fatalf("no-op report = %#v", report)
	}
	if count, _ := st.ChunkCount(); count != 2 {
		t.Fatalf("no-op changed chunks: %d", count)
	}
	changed := testSnapshot("source-a", testDocument("doc-1", SyncChunk{Key: "a", Content: "alpha"}, SyncChunk{Key: "b", Content: "bravo"}))
	_, report = runSync(t, st, changed)
	if report.ReusedChunks != 1 || report.EmbeddedChunks != 1 || report.Chunks.Changed != 1 {
		t.Fatalf("changed report = %#v", report)
	}
}

func TestIncrementalSyncDeletesAndFailureDoesNotPromote(t *testing.T) {
	st, err := New(t.TempDir()+"/sync.db", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first := testSnapshot("source-b", testDocument("keep", SyncChunk{Key: "a", Content: "alpha"}), testDocument("remove", SyncChunk{Key: "b", Content: "beta"}))
	runSync(t, st, first)
	second := testSnapshot("source-b", testDocument("keep", SyncChunk{Key: "a", Content: "alpha"}))
	_, report := runSync(t, st, second)
	if report.Documents.Deleted != 1 || report.DeletedChunks != 1 {
		t.Fatalf("delete report = %#v", report)
	}
	if count, _ := st.ChunkCount(); count != 1 {
		t.Fatalf("chunks after deletion = %d", count)
	}
	task, _, err := st.SubmitSync(testSnapshot("source-b", testDocument("keep", SyncChunk{Key: "a", Content: "changed"})), "failure", 0)
	if err != nil {
		t.Fatal(err)
	}
	claimed, snapshot, ok, err := st.ClaimNextSyncTask()
	if err != nil || !ok {
		t.Fatalf("claim failed: %v %v", ok, err)
	}
	diff, err := st.PrepareSyncDiff(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PromoteSync(claimed, snapshot, diff); err == nil {
		t.Fatal("promotion without staged vectors succeeded")
	}
	if err := st.FailSyncTask(task, &SyncError{Code: "embedding_failed", Message: "provider unavailable"}); err != nil {
		t.Fatal(err)
	}
	baseline, err := st.GetSyncBaseline("source-b")
	if err != nil || baseline.Revision != 2 {
		t.Fatalf("baseline after failed promotion = %#v, %v", baseline, err)
	}
	if count, _ := st.ChunkCount(); count != 1 {
		t.Fatalf("failed promotion changed chunks: %d", count)
	}
}

func TestIncrementalSyncRejectsDuplicateDocumentsAndSupportsIdempotency(t *testing.T) {
	st, err := New(t.TempDir()+"/sync.db", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, _, err = st.SubmitSync(testSnapshot("source-c", testDocument("same", SyncChunk{Key: "a", Content: "a"}), testDocument("same", SyncChunk{Key: "b", Content: "b"})), "", 0)
	if SyncErrorCode(err) != "validation" {
		t.Fatalf("duplicate error = %v", err)
	}
	snapshot := testSnapshot("source-c", testDocument("one", SyncChunk{Key: "a", Content: "a"}))
	first, replayed, err := st.SubmitSync(snapshot, "key", 0)
	if err != nil || replayed {
		t.Fatalf("first idempotent submit = %#v %v %v", first, replayed, err)
	}
	second, replayed, err := st.SubmitSync(snapshot, "key", 0)
	if err != nil || !replayed || first.ID != second.ID {
		t.Fatalf("idempotent replay = %#v %v %v", second, replayed, err)
	}
	_, _, err = st.SubmitSync(testSnapshot("source-c", testDocument("two", SyncChunk{Key: "a", Content: "different"})), "key", 0)
	if SyncErrorCode(err) != "idempotency_conflict" {
		t.Fatalf("idempotency conflict = %v", err)
	}
}

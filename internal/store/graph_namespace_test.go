package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wrath-y/local-rag/internal/graphquery"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/operability"
)

func lifecycleStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir()+"/rag.db", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func lifecyclePage(t *testing.T, s *Store, ns string) NamespacePage {
	t.Helper()
	p, e := s.QueryGraphNamespace(context.Background(), ns, NamespaceQuery{})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func lifecycleDelete(t *testing.T, s *Store, ns string) NamespaceDeletionTask {
	t.Helper()
	p := lifecyclePage(t, s, ns)
	task, e := s.BeginGraphNamespaceDeletion(context.Background(), ns, "delete-key", NamespaceDeleteRequest{p.Generation, p.ManifestHash, true})
	if e != nil {
		t.Fatal(e)
	}
	return task
}
func lifecycleCode(t *testing.T, err error, code graphsnapshot.Code) {
	t.Helper()
	var ge *graphsnapshot.Error
	if !errors.As(err, &ge) || ge.Code != code {
		t.Fatalf("error=%v want=%s", err, code)
	}
}
func lifecycleBuild(t *testing.T, s *Store, ns, v string) {
	t.Helper()
	ctx := context.Background()
	nodes := []graphsnapshot.Node{{ID: "node", Type: "person", Label: ns, Text: "namespace " + ns, Properties: []byte(`{}`), Provenance: []byte(`{}`)}}
	edges := []graphsnapshot.Edge{{ID: "edge", From: "node", To: "node", Type: "self", RelationKind: "explicit", Confidence: "1", Properties: []byte(`{}`), Provenance: []byte(`{}`)}}
	_, hash, e := graphsnapshot.CanonicalHash(nodes, edges)
	if e != nil {
		t.Fatal(e)
	}
	service := graphsnapshot.NewService(s, nil)
	if _, ge := service.Put(ctx, ns, v, graphsnapshot.Request{SchemaVersion: "1.0", Mode: graphsnapshot.ModeFull, ContentHash: hash, Nodes: nodes, Edges: edges}); ge != nil {
		t.Fatal(ge)
	}
	task, found, e := s.ClaimOldestQueuedGraphTask(ctx)
	if e != nil || !found {
		t.Fatalf("claim=%v %v", found, e)
	}
	if e = service.Dispatch(ctx, task, graphEmbedderFake{}); e != nil {
		t.Fatal(e)
	}
}

func TestNamespacePaginationAndGenerationRejectChangedScope(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		seedGraphSnapshot(t, s, "A", fmt.Sprintf("v%d", i), "A")
	}
	seedGraphSnapshot(t, s, "B", "v1", "B")
	first, e := s.QueryGraphNamespace(ctx, "A", NamespaceQuery{Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	page := first
	for {
		for _, item := range page.Items {
			if seen[item.Version] {
				t.Fatal("duplicate")
			}
			seen[item.Version] = true
		}
		if page.NextCursor == nil {
			break
		}
		page, e = s.QueryGraphNamespace(ctx, "A", NamespaceQuery{Cursor: *page.NextCursor, Limit: 2})
		if e != nil {
			t.Fatal(e)
		}
		if page.Generation != first.Generation || page.ManifestHash != first.ManifestHash {
			t.Fatal("unstable page")
		}
	}
	if len(seen) != 7 {
		t.Fatalf("enumerated=%d", len(seen))
	}
	seedGraphSnapshot(t, s, "A", "new", "A")
	_, e = s.QueryGraphNamespace(ctx, "A", NamespaceQuery{Cursor: *first.NextCursor, Limit: 2})
	lifecycleCode(t, e, graphsnapshot.CodeNamespaceGenerationConflict)
	_, e = s.BeginGraphNamespaceDeletion(ctx, "A", "key", NamespaceDeleteRequest{first.Generation, first.ManifestHash, true})
	lifecycleCode(t, e, graphsnapshot.CodeNamespaceGenerationConflict)
	_, e = s.QueryGraphNamespace(ctx, "B", NamespaceQuery{Cursor: *first.NextCursor, Limit: 2})
	lifecycleCode(t, e, graphsnapshot.CodeInvalidNamespaceRequest)
}

func TestNamespaceReferenceCASProtectsBothDeletionRoutesAndOwners(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	seedGraphSnapshot(t, s, "A", "v1", "A")
	before := lifecyclePage(t, s, "A")
	ref := []SnapshotReference{{ReferenceID: "backup-1", Version: "v1", Kind: "backup"}}
	result, e := s.SyncGraphNamespaceReferences(ctx, "A", NamespaceReferenceSync{Owner: "backups", ExpectedRevision: 0, References: ref})
	if e != nil || result.Revision != 1 {
		t.Fatalf("sync=%+v err=%v", result, e)
	}
	_, e = s.SyncGraphNamespaceReferences(ctx, "A", NamespaceReferenceSync{Owner: "backups", ExpectedRevision: 0, References: []SnapshotReference{}})
	lifecycleCode(t, e, graphsnapshot.CodeReferenceRevisionConflict)
	if e = s.DeleteGraphSnapshot(ctx, "A", "v1"); e == nil {
		t.Fatal("legacy deletion bypassed references")
	} else {
		lifecycleCode(t, e, graphsnapshot.CodeSnapshotReferenced)
	}
	p := lifecyclePage(t, s, "A")
	_, e = s.BeginGraphNamespaceDeletion(ctx, "A", "key", NamespaceDeleteRequest{p.Generation, p.ManifestHash, true})
	lifecycleCode(t, e, graphsnapshot.CodeSnapshotReferenced)
	if _, e = s.SyncGraphNamespaceReferences(ctx, "A", NamespaceReferenceSync{Owner: "runs", References: []SnapshotReference{{ReferenceID: "run-1", Version: "v1", Kind: "run"}}}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SyncGraphNamespaceReferences(ctx, "A", NamespaceReferenceSync{Owner: "backups", ExpectedRevision: 1, References: []SnapshotReference{}}); e != nil {
		t.Fatal(e)
	}
	p = lifecyclePage(t, s, "A")
	if len(p.References) != 1 || p.References[0].Owner != "runs" {
		t.Fatalf("lost owner protection: %+v", p.References)
	}
	if before.Generation == p.Generation {
		t.Fatal("reference mutations did not advance generation")
	}
	_, e = s.BeginGraphNamespaceDeletion(ctx, "A", "key", NamespaceDeleteRequest{before.Generation, before.ManifestHash, true})
	lifecycleCode(t, e, graphsnapshot.CodeNamespaceGenerationConflict)
}

func TestNamespaceConcurrentReferenceCASAndDuplicateConfirmation(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	seedGraphSnapshot(t, s, "A", "v1", "A")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.SyncGraphNamespaceReferences(ctx, "A", NamespaceReferenceSync{Owner: "owner", References: []SnapshotReference{}})
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else {
			lifecycleCode(t, e, graphsnapshot.CodeReferenceRevisionConflict)
		}
	}
	if successes != 1 {
		t.Fatalf("CAS winners=%d", successes)
	}
	p := lifecyclePage(t, s, "A")
	request := NamespaceDeleteRequest{p.Generation, p.ManifestHash, true}
	tasks := make(chan NamespaceDeletionTask, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, e := s.BeginGraphNamespaceDeletion(ctx, "A", "same", request)
			tasks <- task
			errs <- e
		}()
	}
	wg.Wait()
	close(tasks)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for task := range tasks {
		if id != "" && id != task.TaskID {
			t.Fatal("duplicate task")
		}
		id = task.TaskID
	}
	_, e := s.BeginGraphNamespaceDeletion(ctx, "A", "different", request)
	lifecycleCode(t, e, graphsnapshot.CodeIdempotencyConflict)
}

func TestNamespacePurgeRemovesAllDerivedRowsAndPreservesOtherNamespace(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	lifecycleBuild(t, s, "A", "v1")
	lifecycleBuild(t, s, "B", "v1")
	for _, ns := range []string{"A", "B"} {
		if _, e := s.ActivateGraphSnapshot(ctx, ns, "v1"); e != nil {
			t.Fatal(e)
		}
	}
	components := []operability.Component{operability.ComponentGraphIndexes, operability.ComponentFTS, operability.ComponentVector}
	if _, _, e := s.AdmitGraphRebuild(ctx, "A", "v1", "rebuild", operability.RequestFingerprint(components), "", "rebuild-A", components); e != nil {
		t.Fatal(e)
	}
	task, found, e := s.ClaimOldestQueuedGraphTask(ctx)
	if e != nil || !found {
		t.Fatal(e)
	}
	if e = s.ProcessGraphRebuild(ctx, task, graphEmbedderFake{}); e != nil {
		t.Fatal(e)
	}
	before, e := s.ReadGraphSnapshot(ctx, "B", "v1")
	if e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"graph_nodes", "graph_edges", "graph_index_adjacency", "graph_vector_items", "graph_search_documents"} {
		var count int
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table + ` WHERE namespace='A'`).Scan(&count); err != nil || count == 0 {
			t.Fatalf("fixture missing %s: %d %v", table, count, err)
		}
	}
	deletion := lifecycleDelete(t, s, "A")
	if e = s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, e := s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if e != nil || !receipt.Complete || len(receipt.RemainingArtifacts) != 0 {
		t.Fatalf("receipt=%+v err=%v", receipt, e)
	}
	for _, table := range []string{"graph_namespaces", "graph_snapshots", "graph_nodes", "graph_edges", "graph_namespace_heads", "graph_snapshot_components", "graph_snapshot_staging", "graph_search_documents", "graph_vector_items", "graph_retrieval_generations", "graph_index_adjacency", "graph_tasks", "graph_reference_owners", "graph_snapshot_references"} {
		var n int
		if e = s.DB().QueryRow(`SELECT count(*) FROM ` + table + ` WHERE namespace='A'`).Scan(&n); e != nil || n != 0 {
			t.Fatalf("remaining %s=%d err=%v", table, n, e)
		}
	}
	var orphanVectors int
	if e = s.DB().QueryRow(`SELECT count(*) FROM graph_vectors WHERE item_id NOT IN (SELECT id FROM graph_vector_items)`).Scan(&orphanVectors); e != nil || orphanVectors != 0 {
		t.Fatalf("orphan vectors=%d err=%v", orphanVectors, e)
	}
	if _, e = s.DB().Exec(`INSERT INTO graph_search_fts(graph_search_fts,rank) VALUES('integrity-check',1)`); e != nil {
		t.Fatalf("fts integrity: %v", e)
	}
	after, e := s.ReadGraphSnapshot(ctx, "B", "v1")
	if e != nil || after.ContentHash != before.ContentHash || len(after.Nodes) != len(before.Nodes) {
		t.Fatalf("B changed: %v", e)
	}
	active, e := s.LookupActiveGraphSnapshot(ctx, "B")
	if e != nil || active.ActiveVersion == nil || *active.ActiveVersion != "v1" {
		t.Fatal("B head changed")
	}
	if e = s.WithRead(ctx, "B", "v1", func(view graphquery.ReadView) error {
		nodes, e := view.Nodes(ctx, []string{"node"})
		if e == nil && (len(nodes) != 1 || nodes[0].Label != "B") {
			return errors.New("B wrong read")
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	// A delayed legacy snapshot admission cannot resurrect a deleted identity.
	_, hash, _ := graphsnapshot.CanonicalHash(nil, nil)
	_, e = s.AcceptGraphSnapshot(ctx, graphsnapshot.AcceptedSnapshot{Namespace: "A", Version: "late", TaskID: "late", ContentHash: hash, Manifest: graphsnapshot.Manifest{SchemaVersion: "1.0"}})
	lifecycleCode(t, e, graphsnapshot.CodeNamespaceDeleting)
}

func TestNamespaceDeleteDrainsWriterAndReadCacheBeforeCompletion(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	lifecycleBuild(t, s, "A", "v1")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- s.WithRead(ctx, "A", "v1", func(view graphquery.ReadView) error {
			if _, e := view.Nodes(ctx, []string{"node"}); e != nil {
				return e
			}
			close(entered)
			<-release
			nodes, e := view.Nodes(ctx, []string{"node"})
			if e == nil && len(nodes) != 1 {
				return errors.New("in-flight cache lost")
			}
			return e
		})
	}()
	<-entered
	deletion := lifecycleDelete(t, s, "A")
	if e := s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, _ := s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if receipt.Complete {
		t.Fatal("completed before reader/cache drained")
	}
	if e := s.CheckGraphNamespaceAccess(ctx, "A"); e == nil {
		t.Fatal("new read allowed")
	}
	close(release)
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	if e := s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, _ = s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if !receipt.Complete {
		t.Fatalf("not complete: %+v", receipt)
	}
	// A second namespace contains a previously accepted queued writer.
	nodes := []graphsnapshot.Node{{ID: "node", Type: "kind", Label: "node", Text: "text", Properties: []byte(`{}`), Provenance: []byte(`{}`)}}
	_, hash, _ := graphsnapshot.CanonicalHash(nodes, nil)
	service := graphsnapshot.NewService(s, nil)
	if _, ge := service.Put(ctx, "C", "v1", graphsnapshot.Request{SchemaVersion: "1.0", Mode: graphsnapshot.ModeFull, ContentHash: hash, Nodes: nodes}); ge != nil {
		t.Fatal(ge)
	}
	deletion = lifecycleDelete(t, s, "C")
	if e := s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, _ = s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if receipt.State != "draining" {
		t.Fatalf("writer not draining: %+v", receipt)
	}
	task, found, e := s.ClaimOldestQueuedGraphTask(ctx)
	if e != nil || !found {
		t.Fatal(e)
	}
	if e = service.Dispatch(ctx, task, graphEmbedderFake{}); e != nil {
		t.Fatal(e)
	}
	if e = s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, _ = s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if !receipt.Complete {
		t.Fatalf("writer did not settle: %+v", receipt)
	}
}

func TestNamespaceInterruptedPurgeRollsBackAndResumesAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/rag.db"
	s, e := New(path, 4)
	if e != nil {
		t.Fatal(e)
	}
	lifecycleBuild(t, s, "A", "v1")
	lifecycleBuild(t, s, "B", "v1")
	deletion := lifecycleDelete(t, s, "A")
	s.graphNamespaceDeleteFailpoint = func(boundary string) error {
		if boundary == "after_purge" {
			return errors.New("simulated process interruption")
		}
		return nil
	}
	if e = s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, _ := s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if receipt.State != "failed" {
		t.Fatalf("failure not durable: %+v", receipt)
	}
	if _, found, e := s.LookupGraphSnapshot(ctx, "A", "v1"); e != nil || !found {
		t.Fatal("partial transaction leaked")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = New(path, 4)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.CheckGraphNamespaceAccess(ctx, "A"); e == nil {
		t.Fatal("restart lost gate")
	}
	if _, e = s.RetryGraphNamespaceDeletion(ctx, deletion.TaskID); e != nil {
		t.Fatal(e)
	}
	if e = s.ProcessGraphNamespaceDeletions(ctx); e != nil {
		t.Fatal(e)
	}
	receipt, _ = s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
	if !receipt.Complete {
		t.Fatalf("resume=%+v", receipt)
	}
	replay, e := s.BeginGraphNamespaceDeletion(ctx, "A", "delete-key", NamespaceDeleteRequest{deletion.Generation, deletion.ManifestHash, true})
	if e != nil || !replay.Replayed || replay.TaskID != deletion.TaskID {
		t.Fatalf("replay=%+v err=%v", replay, e)
	}
	if _, found, e := s.LookupGraphSnapshot(ctx, "B", "v1"); e != nil || !found {
		t.Fatal("restart deleted B")
	}
}

func TestNamespaceMigrationPreservesLegacySnapshotsAndRollbackGuards(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/rag.db"
	old, err := newWithGraphMigrations(path, 4, graphMigrations[:4])
	if err != nil {
		t.Fatal(err)
	}
	seedGraphSnapshot(t, old, "legacy", "v1", "original")
	before, err := old.ReadGraphSnapshot(ctx, "legacy", "v1")
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	upgraded, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	page := lifecyclePage(t, upgraded, "legacy")
	if len(page.Items) != 1 || page.Generation == "0" {
		t.Fatalf("migration omitted old snapshot: %+v", page)
	}
	if _, err = upgraded.SyncGraphNamespaceReferences(ctx, "legacy", NamespaceReferenceSync{Owner: "backups", References: []SnapshotReference{{ReferenceID: "backup", Version: "v1", Kind: "backup"}}}); err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	// Reopen with graph/1..4 migrations only, retaining additive tables and
	// triggers. This simulates disabled cleanup without destructive downgrade.
	old, err = newWithGraphMigrations(path, 4, graphMigrations[:4])
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	after, err := old.ReadGraphSnapshot(ctx, "legacy", "v1")
	if err != nil || before.ContentHash != after.ContentHash || before.Nodes[0].Text != after.Nodes[0].Text {
		t.Fatalf("legacy data changed: %v", err)
	}
	if _, err = old.DB().Exec(`DELETE FROM graph_snapshots WHERE namespace='legacy' AND version='v1'`); err == nil {
		t.Fatal("rollback bypassed persistent reference protection")
	}
}

func TestNamespaceBulkAdmissionDoesNotUpgradeReadTransaction(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	service := graphsnapshot.NewService(s, nil)
	if err := service.Start(ctx, func(ctx context.Context, task graphsnapshot.Task) error { return service.Dispatch(ctx, task, nil) }); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	_, hash, err := graphsnapshot.CanonicalHash(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The worker commits concurrently. All writes must be admitted without a
	// SQLITE_BUSY read-to-write upgrade, and the captured list must be complete.
	for i := 0; i < 101; i++ {
		if _, ge := service.Put(ctx, "bulk", fmt.Sprintf("v%03d", i), graphsnapshot.Request{SchemaVersion: "1.0", Mode: graphsnapshot.ModeFull, ContentHash: hash}); ge != nil {
			t.Fatalf("admission %d: %v cause=%v", i, ge, ge.Cause)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var running int
		if err = s.DB().QueryRow(`SELECT count(*) FROM graph_tasks WHERE namespace='bulk' AND state IN ('queued','running')`).Scan(&running); err != nil {
			t.Fatal(err)
		}
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d accepted writers did not settle", running)
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, err := s.QueryGraphNamespace(ctx, "bulk", NamespaceQuery{Limit: 100})
	if err != nil || len(page.Items) != 100 || page.NextCursor == nil {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = s.QueryGraphNamespace(ctx, "bulk", NamespaceQuery{Cursor: *page.NextCursor, Limit: 100})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("tail page=%+v err=%v", page, err)
	}
}

func TestNamespaceWorkerUnexpectedErrorSettlesBeforeCleanup(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	service := graphsnapshot.NewService(s, nil)
	_, hash, _ := graphsnapshot.CanonicalHash(nil, nil)
	if _, ge := service.Put(ctx, "broken", "v1", graphsnapshot.Request{SchemaVersion: "1.0", Mode: graphsnapshot.ModeFull, ContentHash: hash}); ge != nil {
		t.Fatal(ge)
	}
	deletion := lifecycleDelete(t, s, "broken")
	if err := service.Start(ctx, func(context.Context, graphsnapshot.Task) error {
		return errors.New("injected unexpected dispatch failure")
	}); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := s.ProcessGraphNamespaceDeletions(ctx); err != nil {
			t.Fatal(err)
		}
		receipt, err := s.LookupGraphNamespaceDeletion(ctx, deletion.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed writer stranded cleanup: %+v", receipt)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNamespaceDispatchFallbackPreservesReadySnapshotAndTerminalTasks(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	lifecycleBuild(t, s, "ready", "v1")
	snapshot, found, err := s.LookupGraphSnapshot(ctx, "ready", "v1")
	if err != nil || !found {
		t.Fatal(err)
	}
	task, found, err := s.LookupGraphTask(ctx, snapshot.TaskID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err = s.RecordGraphTaskDispatchFailure(ctx, task, errors.New("late callback error")); err != nil {
		t.Fatal(err)
	}
	terminal, _, _ := s.LookupGraphTask(ctx, task.ID)
	if terminal.State != graphsnapshot.TaskSucceeded || terminal.Error != nil {
		t.Fatal("terminal task changed")
	}
	// The required graph+FTS are ready when optional vector/outcome persistence
	// fails. The fallback must retain readiness and expose a warning.
	if _, err = s.DB().Exec(`UPDATE graph_tasks SET state='running' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`UPDATE graph_snapshot_components SET state='pending' WHERE namespace='ready' AND version='v1' AND component='vector'`); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordGraphTaskDispatchFailure(ctx, task, errors.New("optional failure")); err != nil {
		t.Fatal(err)
	}
	ready, _, _ := s.LookupGraphSnapshot(ctx, "ready", "v1")
	terminal, _, _ = s.LookupGraphTask(ctx, task.ID)
	if !ready.QueryReady || terminal.State != graphsnapshot.TaskSucceeded || len(terminal.Warnings) == 0 {
		t.Fatal("ready snapshot was downgraded")
	}
	// A failed rebuild must retain the source and selected generations.
	components := []operability.Component{operability.ComponentFTS}
	rebuild, _, err := s.AdmitGraphRebuild(ctx, "ready", "v1", "key", operability.RequestFingerprint(components), "", "rebuild-fallback", components)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := s.ClaimOldestQueuedGraphTask(ctx)
	if err != nil || !found || claimed.ID != rebuild.ID {
		t.Fatal(err)
	}
	if err = s.RecordGraphTaskDispatchFailure(ctx, claimed, errors.New("rebuild failure")); err != nil {
		t.Fatal(err)
	}
	ready, _, _ = s.LookupGraphSnapshot(ctx, "ready", "v1")
	terminal, _, _ = s.LookupGraphTask(ctx, rebuild.ID)
	if !ready.QueryReady || ready.ContentHash != snapshot.ContentHash || terminal.State != graphsnapshot.TaskFailed {
		t.Fatal("rebuild fallback changed source")
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Wrath-y/local-rag/internal/graphquery"
)

func readyActivationFixture(t *testing.T, s *Store, namespace, version string) {
	t.Helper()
	seedGraphSnapshot(t, s, namespace, version, namespace+"-"+version)
	if _, err := s.DB().Exec(`UPDATE graph_snapshots SET status='ready',query_ready=1 WHERE namespace=? AND version=?`, namespace, version); err != nil {
		t.Fatal(err)
	}
}

func TestConditionalActivationCompetesAcrossIndependentConnections(t *testing.T) {
	for _, initial := range []string{"", "base"} {
		t.Run("initial_"+initial, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rag.db")
			first, err := New(path, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			for _, version := range []string{"base", "left", "right"} {
				readyActivationFixture(t, first, "project", version)
			}
			if initial != "" {
				if _, err = first.ActivateGraphSnapshot(context.Background(), "project", initial); err != nil {
					t.Fatal(err)
				}
			}
			second, err := New(path, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			start := make(chan struct{})
			errorsOut := make(chan error, 2)
			for i, st := range []*Store{first, second} {
				go func(st *Store, version string) {
					<-start
					changed, err := st.ActivateGraphSnapshotConditional(context.Background(), "project", version, stringPointer(initial))
					if err == nil && !changed {
						err = errors.New("winner did not change head")
					}
					errorsOut <- err
				}(st, []string{"left", "right"}[i])
			}
			close(start)
			wins, conflicts := 0, 0
			for i := 0; i < 2; i++ {
				err := <-errorsOut
				var conflict *GraphActiveVersionConflict
				if err == nil {
					wins++
				} else if errors.As(err, &conflict) {
					conflicts++
				} else {
					t.Fatalf("unexpected competition error: %v", err)
				}
			}
			if wins != 1 || conflicts != 1 {
				t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
			}
			active, err := first.LookupActiveGraphSnapshot(context.Background(), "project")
			if err != nil || active.ActiveVersion == nil || active.ContentHash == nil {
				t.Fatalf("active=%+v err=%v", active, err)
			}
			if _, err := first.ActivateGraphSnapshotConditional(context.Background(), "project", *active.ActiveVersion, stringPointer(initial)); err == nil {
				t.Fatal("stale expectation became an idempotent success")
			}
			changed, err := first.ActivateGraphSnapshotConditional(context.Background(), "project", *active.ActiveVersion, active.ActiveVersion)
			if err != nil || changed {
				t.Fatalf("exact replay changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestConditionalActivationKeepsLegacySnapshotsAndHeadsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	legacy, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	readyActivationFixture(t, legacy, "project", "legacy")
	readyActivationFixture(t, legacy, "project", "next")
	if _, err = legacy.ActivateGraphSnapshot(ctx, "project", "legacy"); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, found, err := legacy.LookupGraphSnapshot(ctx, "project", "legacy")
	if err != nil || !found {
		t.Fatalf("legacy snapshot missing: %v", err)
	}
	var migrations int
	if err = legacy.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := New(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	active, err := upgraded.LookupActiveGraphSnapshot(ctx, "project")
	if err != nil || active.ActiveVersion == nil || *active.ActiveVersion != "legacy" || active.ContentHash == nil || *active.ContentHash != oldSnapshot.ContentHash {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	var after int
	if err = upgraded.DB().QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&after); err != nil || after != migrations {
		t.Fatalf("unexpected schema change: %d -> %d, %v", migrations, after, err)
	}
	if _, err = upgraded.ActivateGraphSnapshotConditional(ctx, "project", "next", active.ActiveVersion); err != nil {
		t.Fatal(err)
	}
	if err = upgraded.WithRead(ctx, "project", "legacy", func(view graphquery.ReadView) error {
		if view.Version() != "legacy" || view.ContentHash() != oldSnapshot.ContentHash {
			t.Fatal("legacy explicit read changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Falling back is an explicit legacy operation; it remains idempotent.
	if changed, err := upgraded.ActivateGraphSnapshot(ctx, "project", "legacy"); err != nil || !changed {
		t.Fatalf("legacy rollback changed=%v err=%v", changed, err)
	}
	if changed, err := upgraded.ActivateGraphSnapshot(ctx, "project", "legacy"); err != nil || changed {
		t.Fatalf("legacy replay changed=%v err=%v", changed, err)
	}
}

func TestActiveReadDoesNotInferPublicationFromReadySnapshot(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "rag.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	readyActivationFixture(t, s, "ready-only", "ready")
	for _, namespace := range []string{"missing", "ready-only"} {
		active, err := s.LookupActiveGraphSnapshot(context.Background(), namespace)
		if err != nil || active.ActiveVersion != nil || active.ContentHash != nil {
			t.Fatalf("active=%+v err=%v", active, err)
		}
	}
}

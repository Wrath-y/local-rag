package store

// Additive lifecycle state deliberately survives namespace deletion. This
// compact receipt prevents delayed writes from recreating a deleted identity.
func init() {
	graphMigrations = append(graphMigrations, graphMigration{component: "namespace_lifecycle", version: 1, sql: `
CREATE TABLE graph_namespace_lifecycle (
 namespace TEXT PRIMARY KEY, generation INTEGER NOT NULL DEFAULT 1,
 state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','deleting','deleted'))
);
INSERT INTO graph_namespace_lifecycle(namespace) SELECT namespace FROM graph_namespaces;
CREATE TABLE graph_reference_owners (
 namespace TEXT NOT NULL, owner TEXT NOT NULL, revision INTEGER NOT NULL,
 PRIMARY KEY(namespace,owner),
 FOREIGN KEY(namespace) REFERENCES graph_namespaces(namespace) ON DELETE CASCADE
);
CREATE TABLE graph_snapshot_references (
 namespace TEXT NOT NULL, owner TEXT NOT NULL, reference_id TEXT NOT NULL, version TEXT NOT NULL, kind TEXT NOT NULL,
 PRIMARY KEY(namespace,owner,reference_id),
 FOREIGN KEY(namespace,owner) REFERENCES graph_reference_owners(namespace,owner) ON DELETE CASCADE,
 FOREIGN KEY(namespace,version) REFERENCES graph_snapshots(namespace,version) ON DELETE RESTRICT
);
CREATE TABLE graph_namespace_deletion_tasks (
 id TEXT PRIMARY KEY, namespace TEXT NOT NULL UNIQUE, idempotency_key TEXT NOT NULL,
 request_hash TEXT NOT NULL, manifest_hash TEXT NOT NULL, generation TEXT NOT NULL,
 scope_json TEXT NOT NULL CHECK(json_valid(scope_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','draining','complete','failed')),
 phase TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TRIGGER graph_namespace_admission BEFORE INSERT ON graph_namespaces
WHEN EXISTS(SELECT 1 FROM graph_namespace_lifecycle WHERE namespace=NEW.namespace AND state<>'active')
BEGIN SELECT RAISE(ABORT,'NAMESPACE_DELETING'); END;
CREATE TRIGGER graph_namespace_created AFTER INSERT ON graph_namespaces
BEGIN INSERT INTO graph_namespace_lifecycle(namespace) VALUES(NEW.namespace) ON CONFLICT(namespace) DO NOTHING; END;
CREATE TRIGGER graph_snapshot_admission BEFORE INSERT ON graph_snapshots
WHEN EXISTS(SELECT 1 FROM graph_namespace_lifecycle WHERE namespace=NEW.namespace AND state<>'active')
BEGIN SELECT RAISE(ABORT,'NAMESPACE_DELETING'); END;
CREATE TRIGGER graph_task_admission BEFORE INSERT ON graph_tasks
WHEN EXISTS(SELECT 1 FROM graph_namespace_lifecycle WHERE namespace=NEW.namespace AND state<>'active')
BEGIN SELECT RAISE(ABORT,'NAMESPACE_DELETING'); END;
CREATE TRIGGER graph_head_admission_insert BEFORE INSERT ON graph_namespace_heads
WHEN EXISTS(SELECT 1 FROM graph_namespace_lifecycle WHERE namespace=NEW.namespace AND state<>'active')
BEGIN SELECT RAISE(ABORT,'NAMESPACE_DELETING'); END;
CREATE TRIGGER graph_head_admission_update BEFORE UPDATE ON graph_namespace_heads
WHEN EXISTS(SELECT 1 FROM graph_namespace_lifecycle WHERE namespace=NEW.namespace AND state<>'active')
BEGIN SELECT RAISE(ABORT,'NAMESPACE_DELETING'); END;
CREATE TRIGGER graph_snapshot_reference_protection BEFORE DELETE ON graph_snapshots
WHEN EXISTS(SELECT 1 FROM graph_snapshot_references WHERE namespace=OLD.namespace AND version=OLD.version)
BEGIN SELECT RAISE(ABORT,'SNAPSHOT_REFERENCED'); END;
CREATE TRIGGER graph_snapshot_generation_insert AFTER INSERT ON graph_snapshots
BEGIN UPDATE graph_namespace_lifecycle SET generation=generation+1 WHERE namespace=NEW.namespace; END;
CREATE TRIGGER graph_snapshot_generation_delete AFTER DELETE ON graph_snapshots
BEGIN UPDATE graph_namespace_lifecycle SET generation=generation+1 WHERE namespace=OLD.namespace; END;
CREATE TRIGGER graph_head_generation_insert AFTER INSERT ON graph_namespace_heads
BEGIN UPDATE graph_namespace_lifecycle SET generation=generation+1 WHERE namespace=NEW.namespace; END;
CREATE TRIGGER graph_head_generation_update AFTER UPDATE ON graph_namespace_heads
BEGIN UPDATE graph_namespace_lifecycle SET generation=generation+1 WHERE namespace=NEW.namespace; END;
CREATE TRIGGER graph_head_generation_delete AFTER DELETE ON graph_namespace_heads
BEGIN UPDATE graph_namespace_lifecycle SET generation=generation+1 WHERE namespace=OLD.namespace; END;
`})
}

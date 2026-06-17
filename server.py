import hashlib
import io
import os
import pickle
import re
import shutil
import time
import zipfile
from datetime import datetime
import numpy as np
import faiss
import yaml
from contextlib import asynccontextmanager
from fastapi import FastAPI, File, HTTPException, UploadFile
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer, CrossEncoder
from rank_bm25 import BM25Okapi
from typing import List, Dict, Optional

import storage
import wal as wal_mod
import metrics
import backup
from obs import structured_log

# ================= CONFIG =================
_dir = os.path.dirname(os.path.abspath(__file__))

with open(os.path.join(_dir, "config.yaml"), "r") as f:
    config = yaml.safe_load(f)

MODEL_NAME = config["model"]["name"]
CHUNK_MIN = config["chunk"]["min_tokens"]
CHUNK_MAX = config["chunk"]["max_tokens"]
# 顶层分块策略：fixed | structure | semantic | agentic（运行时可通过 /config/chunk-strategy 切换）
_VALID_CHUNK_STRATEGIES = {"fixed", "structure", "semantic", "agentic"}
CHUNK_STRATEGY: str = str(config["chunk"].get("strategy", "fixed")).lower()
if CHUNK_STRATEGY not in _VALID_CHUNK_STRATEGIES:
    print(f"[chunk] invalid strategy {CHUNK_STRATEGY!r} in config, falling back to 'fixed'")
    CHUNK_STRATEGY = "fixed"
# 是否启用结构感知（Markdown）分块；非 Markdown 文本仍走原有的句子级逻辑
STRUCTURE_AWARE_CHUNK = bool(config["chunk"].get("structure_aware", True))
# 语义分块参数（仅在 strategy=semantic 时生效）
_sem_cfg = config["chunk"].get("semantic", {}) or {}
SEMANTIC_THRESHOLD_PERCENTILE: int = int(_sem_cfg.get("threshold_percentile", 90))
SEMANTIC_MIN_CHUNK_SIZE: int = int(_sem_cfg.get("min_chunk_size", 2))
SEMANTIC_MAX_CHUNK_SIZE: int = int(_sem_cfg.get("max_chunk_size", 20))
# Agentic 分块参数（仅在 strategy=agentic 时生效；通过 LLM 智能判定 chunk 边界）
_agentic_cfg = config["chunk"].get("agentic", {}) or {}
AGENTIC_GENERATE_SUMMARY: bool = bool(_agentic_cfg.get("generate_summary", True))
AGENTIC_MAX_LLM_INPUT_TOKENS: int = int(_agentic_cfg.get("max_llm_input_tokens", 4000))
# 层次化（Parent-Child）分块配置：FAISS 检索 child（小块），命中后返回 parent（大块）给 LLM
_hier_cfg = config["chunk"].get("hierarchical", {}) or {}
HIERARCHICAL_ENABLED: bool = bool(_hier_cfg.get("enabled", False))
PARENT_MAX_TOKENS: int = int(_hier_cfg.get("parent_max_tokens", 800))
# 上下文强化（标题面包屑前缀）：仅在结构感知分块路径中生效，默认关闭
_ctx_cfg = config["chunk"].get("context_prefix", {}) or {}
CONTEXT_PREFIX_ENABLED: bool = bool(_ctx_cfg.get("enabled", False))
CONTEXT_PREFIX_FORMAT: str = str(_ctx_cfg.get("format", "breadcrumb"))
CONTEXT_PREFIX_MAX_DEPTH: int = int(_ctx_cfg.get("max_depth", 3))
TOP_K = config["retrieve"]["top_k"]
CONTEXT_WINDOW = config["retrieve"].get("context_window", 180000)
RESPONSE_RESERVE = config["retrieve"].get("response_reserve", 8000)
AVG_CHUNK_TOKENS = (CHUNK_MIN + CHUNK_MAX) // 2  # 每个 chunk 的平均 token 数估算
INDEX_PATH = os.path.join(_dir, config["storage"]["index_path"])
TEXTS_PATH = os.path.join(_dir, config["storage"]["texts_path"])
DATA_DIR = _dir
STORAGE_DIR = os.path.join(_dir, "storage")
MANIFEST_PATH = storage.manifest_path_for(STORAGE_DIR)
WAL_PATH = wal_mod.wal_path_for(STORAGE_DIR)

_wal_cfg = config["storage"].get("wal", {})
WAL_ENABLED: bool = bool(_wal_cfg.get("enabled", True))
WAL_MAX_SIZE_BYTES: int = int(float(_wal_cfg.get("max_size_mb", 10)) * 1024 * 1024)

DISK_FREE_ERROR_BYTES: int = 1 * 1024 * 1024 * 1024  # /health → error if free < 1GB

_backup_cfg = config["storage"].get("backup", {})
BACKUP_ENABLED: bool = bool(_backup_cfg.get("enabled", True))
BACKUP_CRON: str = str(_backup_cfg.get("schedule", "0 3 * * *"))
_retention = _backup_cfg.get("retention", {})
BACKUP_RETENTION_DAYS: int = int(_retention.get("days", 7))
BACKUP_RETENTION_WEEKS: int = int(_retention.get("weeks", 4))
BACKUPS_DIR: str = os.path.join(_dir, "backups")
_backup_timer = None  # threading.Timer handle


def _safe_disk_free() -> int:
    try:
        return shutil.disk_usage(DATA_DIR).free
    except OSError:
        return -1
DOC_PREFIX = config["embedding"]["doc_prefix"]
QUERY_PREFIX = config["embedding"]["query_prefix"]

LOG_LANG = config.get("log", {}).get("lang", "zh")

# ================= I18N =================
_MSGS: dict = {
    "zh": {
        "model_loading":           "[1/3] 加载 embedding 模型：{name} ...",
        "model_loaded":            "[1/3] 模型加载完成，向量维度：{dim}",
        "store_loaded":            "[2/3] 向量库加载完成，已有 {n} 个 chunk",
        "store_empty":             "[2/3] 向量库初始化（空库）",
        "service_ready":           "[3/3] 服务就绪，监听 http://127.0.0.1:{port}",
        "retrieve_query":          "[retrieve] 查询: {q}",
        "retrieve_empty_store":    "[retrieve] 向量库为空，返回空结果",
        "retrieve_dynamic_top_k":  "[retrieve] dynamic_top_k={k}（已用≈{used} tokens，剩余≈{remaining} tokens）",
        "retrieve_faiss":          "[retrieve] FAISS 返回 {k} 个候选（库总量 {n}）",
        "retrieve_threshold":      "[retrieve] 阈值过滤（< {t}）丢弃 {d} 个，剩余 {r} 个",
        "retrieve_rerank_header":  "[retrieve] rerank 后顺序:",
        "retrieve_final":          "[retrieve] 最终返回 {n} 个 chunks",
        "rerank_loading":          "[rerank] 首次开启，加载模型：{model} ...",
        "rerank_loaded":           "[rerank] 模型加载完成",
        "ingest_skip":             "[ingest] 来源 '{source}' 内容未变更，跳过入库",
    },
    "en": {
        "model_loading":           "[1/3] Loading embedding model: {name} ...",
        "model_loaded":            "[1/3] Model loaded, embedding dim: {dim}",
        "store_loaded":            "[2/3] Vector store loaded, {n} chunks",
        "store_empty":             "[2/3] Vector store initialized (empty)",
        "service_ready":           "[3/3] Service ready at http://127.0.0.1:{port}",
        "retrieve_query":          "[retrieve] query: {q}",
        "retrieve_empty_store":    "[retrieve] store is empty, returning no results",
        "retrieve_dynamic_top_k":  "[retrieve] dynamic_top_k={k} (used≈{used} tokens, remaining≈{remaining} tokens)",
        "retrieve_faiss":          "[retrieve] FAISS returned {k} candidates (total: {n})",
        "retrieve_threshold":      "[retrieve] threshold filter (< {t}) discarded {d}, remaining {r}",
        "retrieve_rerank_header":  "[retrieve] after rerank:",
        "retrieve_final":          "[retrieve] returning {n} chunks",
        "rerank_loading":          "[rerank] First enable — loading model: {model} ...",
        "rerank_loaded":           "[rerank] Model loaded",
        "ingest_skip":             "[ingest] source '{source}' unchanged, skipping ingest",
    },
}


def _t(key: str, **kwargs) -> str:
    lang = LOG_LANG if LOG_LANG in _MSGS else "zh"
    template = _MSGS[lang].get(key, key)
    return template.format(**kwargs) if kwargs else template


SCORE_THRESHOLD = 0.45
# 固定 2 句，行为稳定可预期。
OVERLAP_SENTENCES = 2
RERANK_MODEL_NAME = config["rerank"]["model"]

# ================= MODEL =================
print(_t("model_loading", name=MODEL_NAME))
_model_t0 = time.perf_counter()
model = SentenceTransformer(MODEL_NAME)
DIM = model.get_embedding_dimension()
metrics.model_load_seconds.set(time.perf_counter() - _model_t0)
print(_t("model_loaded", dim=DIM))

# rerank 模型：lazy 加载，首次开启时初始化
rerank_enabled: bool = config["rerank"]["enabled"]
reranker: CrossEncoder = None

verbose_enabled: bool = config["retrieve"].get("verbose", True)
dynamic_top_k_enabled: bool = config["retrieve"].get("dynamic_top_k", False)

_qr_cfg = config.get("query_rewrite", {})
query_rewrite_enabled: bool = bool(_qr_cfg.get("enabled", False))
query_rewrite_strategy: str = str(_qr_cfg.get("strategy", "expansion"))

index: faiss.IndexFlatIP = None
stored_chunks: List[Dict] = []
chunk_set: set = set()
bm25: BM25Okapi = None
_emb_cache: Dict[str, np.ndarray] = {}  # text → normalized embedding
_source_hashes: Dict[str, str] = {}  # source → MD5 of full ingested text (Plan A skip)
_stats = {"total_queries": 0, "zero_hit_queries": 0, "total_chunks_returned": 0}

# WAL runtime state (module-scoped, guarded by storage write lock for writes)
_wal_replaying: bool = False
_wal_readonly_reason: "Optional[str]" = None  # type: ignore[name-defined]
_wal_next_seq: int = 0  # incremented on every append

# Index rebuild state
_index_rebuilding: bool = False
_index_rebuild_progress: float = 0.0

# ================= STORAGE =================
def encode_with_cache(texts: List[str]) -> np.ndarray:
    """Encode texts with DOC_PREFIX, returning cache hits instantly and batching misses."""
    result = np.zeros((len(texts), DIM), dtype=np.float32)
    miss_idx: List[int] = []
    miss_prefixed: List[str] = []

    for i, t in enumerate(texts):
        if t in _emb_cache:
            result[i] = _emb_cache[t]
        else:
            miss_idx.append(i)
            miss_prefixed.append(f"{DOC_PREFIX}{t}")

    if miss_prefixed:
        embs = model.encode(miss_prefixed, normalize_embeddings=True, show_progress_bar=False)
        for j, i in enumerate(miss_idx):
            _emb_cache[texts[i]] = embs[j]
            result[i] = embs[j]

    return np.array(result, dtype=np.float32)


def rebuild_bm25():
    global bm25
    if stored_chunks:
        corpus = [_bigrams(c["text"]) or [c["text"].lower()] for c in stored_chunks]
        bm25 = BM25Okapi(corpus)
    else:
        bm25 = None


def _backup_members():
    """Members to include in every backup / restore."""
    return [
        backup.MemberSpec(arcname="chunks.pkl", abs_path=TEXTS_PATH),
        backup.MemberSpec(arcname="index.bin", abs_path=INDEX_PATH),
        backup.MemberSpec(arcname="storage/manifest.json", abs_path=MANIFEST_PATH),
        backup.MemberSpec(arcname="storage/wal.jsonl", abs_path=WAL_PATH),
    ]


def _backup_run_core(name_override: "Optional[str]" = None) -> dict:
    """Pack the four files to a zip under BACKUPS_DIR. Caller MUST hold write_lock.

    Returns `{status, path, size_bytes}`.
    """
    if name_override:
        dst = os.path.join(BACKUPS_DIR, name_override)
    else:
        now = datetime.now()
        day_dir = os.path.join(BACKUPS_DIR, now.strftime("%Y-%m-%d"))
        dst = os.path.join(day_dir, f"rag-{now.strftime('%H%M%S-%f')}.zip")
    size = backup.make_backup(_backup_members(), dst)
    metrics.backup_total.inc()
    metrics.last_backup_timestamp_seconds.set(time.time())
    structured_log("backup_done", path=dst, size_bytes=size)
    return {"status": "ok", "path": dst, "size_bytes": size}


def _prune_backups():
    try:
        deleted = backup.prune(BACKUPS_DIR, BACKUP_RETENTION_DAYS, BACKUP_RETENTION_WEEKS)
        if deleted:
            structured_log("backup_pruned", count=len(deleted))
    except Exception as e:
        print(f"[backup] prune failed: {e}")


def _restore_core(zip_path: str) -> dict:
    """Restore from a zip with transactional pre-restore snapshot + rollback."""
    global _wal_readonly_reason
    import shutil as _shutil
    import tempfile

    if not os.path.isfile(zip_path):
        raise HTTPException(status_code=404, detail=f"backup not found: {zip_path}")

    _wal_readonly_reason = "restore in progress"
    try:
        with storage.write_lock():
            # 1. Snapshot current state first
            pre_restore_name = f"pre-restore-{int(time.time())}.zip"
            pre_restore_path = os.path.join(BACKUPS_DIR, pre_restore_name)
            backup.make_backup(_backup_members(), pre_restore_path)

            # 2. Extract target zip to staging
            staging = tempfile.mkdtemp(prefix="rag_restore_", dir=DATA_DIR)
            try:
                extracted = backup.restore_from(zip_path, staging)

                # 3. Atomic replace each target file
                mapping = {
                    "chunks.pkl": TEXTS_PATH,
                    "index.bin": INDEX_PATH,
                    "storage/manifest.json": MANIFEST_PATH,
                    "storage/wal.jsonl": WAL_PATH,
                }
                for arc, target in mapping.items():
                    src = os.path.join(staging, arc)
                    if os.path.exists(src):
                        os.makedirs(os.path.dirname(os.path.abspath(target)), exist_ok=True)
                        os.replace(src, target)
                    else:
                        # If the backup predates a file (e.g. WAL didn't exist yet),
                        # truncate/remove the current one so the post-restore state
                        # matches the backup's intent. Keeping stale content here
                        # would silently leak post-backup writes through WAL replay.
                        if arc == "storage/wal.jsonl" and os.path.exists(target):
                            wal_mod.truncate_atomic(target)
                        elif arc == "storage/manifest.json" and os.path.exists(target):
                            os.remove(target)

                # 4. Reload — failure triggers rollback
                load_store()
            except Exception:
                # Rollback from pre-restore snapshot
                print("[restore] reload failed, rolling back from pre-restore snapshot")
                try:
                    rollback_staging = tempfile.mkdtemp(prefix="rag_rollback_", dir=DATA_DIR)
                    backup.restore_from(pre_restore_path, rollback_staging)
                    for arc, target in mapping.items():
                        src = os.path.join(rollback_staging, arc)
                        if os.path.exists(src):
                            os.replace(src, target)
                    load_store()
                    _wal_readonly_reason = "restore failed, rolled back — see logs"
                finally:
                    try:
                        _shutil.rmtree(rollback_staging, ignore_errors=True)
                    except Exception:
                        pass
                raise
            finally:
                try:
                    _shutil.rmtree(staging, ignore_errors=True)
                except Exception:
                    pass

            _wal_readonly_reason = None
            return {"status": "ok", "pre_restore": pre_restore_path, "restored_entries": extracted}
    finally:
        # If something set _wal_readonly_reason to "restore in progress" and we didn't clear it,
        # leave it as-is so operator sees the failure.
        pass


def _schedule_next_backup():
    """Register a one-shot Timer for the next cron fire; on fire run backup and re-schedule."""
    global _backup_timer
    if not BACKUP_ENABLED:
        return
    try:
        next_fire_fn = backup.parse_cron(BACKUP_CRON)
        next_ts = next_fire_fn(time.time())
        delay = max(1.0, next_ts - time.time())
    except Exception as e:
        print(f"[backup] failed to parse cron {BACKUP_CRON!r}: {e}")
        return

    import threading as _threading

    def _fire():
        global _backup_timer
        try:
            with storage.write_lock():
                _backup_run_core()
                _prune_backups()
        except Exception as e:
            print(f"[backup] scheduled run failed: {e}")
        # Re-schedule next one regardless of success
        _schedule_next_backup()

    _backup_timer = _threading.Timer(delay, _fire)
    _backup_timer.daemon = True
    _backup_timer.start()


def _cancel_backup_timer():
    global _backup_timer
    if _backup_timer is not None:
        try:
            _backup_timer.cancel()
        except Exception:
            pass
        _backup_timer = None


def _rebuild_index_core(chunks_list, batch_size: int = 64):
    """Rebuild a FAISS index from chunks by re-encoding in batches.

    Updates _index_rebuild_progress and the reindex metric each batch.
    Returns the new IndexFlatIP (caller swaps the global ref under write lock).
    """
    global _index_rebuild_progress
    new_index = faiss.IndexFlatIP(DIM)
    total = len(chunks_list)
    if total == 0:
        _index_rebuild_progress = 1.0
        metrics.reindex_progress_ratio.set(1.0)
        return new_index

    texts = [c["text"] for c in chunks_list]
    encoded = 0
    for i in range(0, total, batch_size):
        batch = texts[i : i + batch_size]
        prefixed = [f"{DOC_PREFIX}{t}" for t in batch]
        vecs = model.encode(prefixed, normalize_embeddings=True, show_progress_bar=False)
        vecs = np.array(vecs, dtype=np.float32)
        new_index.add(vecs)
        # Refresh embedding cache with freshly computed vectors
        for j, t in enumerate(batch):
            _emb_cache[t] = vecs[j]
        encoded += len(batch)
        _index_rebuild_progress = encoded / total
        metrics.reindex_progress_ratio.set(_index_rebuild_progress)
    return new_index


def _rebuild_index_sync():
    """Startup self-heal path: rebuild from in-memory stored_chunks synchronously."""
    global index
    new_index = _rebuild_index_core(stored_chunks)
    storage.atomic_write_faiss(INDEX_PATH, new_index)
    index = new_index
    save_store(new_index=new_index, new_chunks=stored_chunks,
               wal_offset=wal_mod.file_size(WAL_PATH) if WAL_ENABLED else 0,
               wal_seq=_wal_next_seq)
    print(f"[index] rebuilt from {len(stored_chunks)} chunks")


def _rebuild_index_async():
    """Background rebuild triggered by POST /index/rebuild."""
    global index, _index_rebuilding, _wal_readonly_reason, _index_rebuild_progress
    try:
        with storage.write_lock():
            new_index = _rebuild_index_core(stored_chunks)
            save_store(new_index=new_index, new_chunks=stored_chunks,
                       wal_offset=wal_mod.file_size(WAL_PATH) if WAL_ENABLED else 0,
                       wal_seq=_wal_next_seq)
            index = new_index
        print(f"[index] rebuild completed, ntotal={new_index.ntotal}")
    except Exception as e:
        print(f"[index] rebuild failed: {e}")
        _wal_readonly_reason = f"index rebuild failed: {e}"
    finally:
        _index_rebuilding = False
        _index_rebuild_progress = 0.0
        metrics.reindex_progress_ratio.set(0.0)
        # If readonly reason was set by rebuild kickoff AND rebuild succeeded, clear it.
        if _wal_readonly_reason == "index rebuild in progress":
            _wal_readonly_reason = None


def _apply_wal_record(record) -> None:
    """Re-apply a single WAL record via the internal *_core helpers.

    Caller MUST hold storage.write_lock(). Raises on business failure.
    """
    if record.op == "ingest":
        _ingest_core(record.payload["text"], record.payload.get("source", "unknown"))
    elif record.op == "delete_source":
        try:
            _delete_source_core(record.payload["name"])
        except HTTPException as e:
            # Source may have been deleted by a later op or already removed; skip gracefully.
            if e.status_code == 404:
                print(f"[wal] replay: delete_source '{record.payload['name']}' already absent, skipping")
            else:
                raise
    elif record.op == "reset":
        _reset_core()
    else:
        raise RuntimeError(f"unknown WAL op: {record.op!r}")


def _replay_wal_if_needed(manifest) -> None:
    """Replay uncommitted WAL entries under the write lock.

    Sets _wal_replaying / _wal_readonly_reason as needed. Truncates WAL on success.
    """
    global _wal_replaying, _wal_readonly_reason, _wal_next_seq
    if not WAL_ENABLED:
        return
    if not os.path.exists(WAL_PATH):
        return

    committed_offset = manifest.wal.committed_offset if manifest is not None else 0
    wal_size = wal_mod.file_size(WAL_PATH)
    if wal_size <= committed_offset:
        # Set _wal_next_seq so future appends continue from the last committed seq
        if manifest is not None:
            _wal_next_seq = manifest.wal.committed_seq
        return

    print(f"[wal] replaying ops from offset {committed_offset} (size={wal_size})")
    _wal_replaying = True
    metrics.wal_replaying.set(1)
    structured_log("wal_replay_start", committed_offset=committed_offset, wal_size=wal_size)
    replayed = 0
    last_seq = manifest.wal.committed_seq if manifest is not None else 0

    try:
        with storage.write_lock():
            for start, end, record, err in wal_mod.iter_records(WAL_PATH, committed_offset):
                if err is not None:
                    _wal_readonly_reason = f"wal corrupt at offset {err.offset}: {err}"
                    print(f"[wal] {_wal_readonly_reason}")
                    return
                try:
                    _apply_wal_record(record)
                except Exception as e:
                    _wal_readonly_reason = f"replay failed at seq {record.seq}: {e}"
                    print(f"[wal] {_wal_readonly_reason}")
                    return
                last_seq = record.seq
                replayed += 1

            # All replayed successfully — checkpoint and truncate.
            _wal_next_seq = last_seq
            save_store(new_index=index, new_chunks=stored_chunks, wal_offset=0, wal_seq=last_seq)
            wal_mod.truncate_atomic(WAL_PATH)
            print(f"[wal] replayed {replayed} ops, WAL truncated")
            structured_log("wal_replay_done", replayed=replayed, last_seq=last_seq)
    finally:
        _wal_replaying = False
        metrics.wal_replaying.set(0)


def load_store():
    global index, stored_chunks, chunk_set, _wal_next_seq
    _emb_cache.clear()
    _source_hashes.clear()

    # 清理上次崩溃残留的临时文件，避免干扰判断
    removed = storage.cleanup_orphan_tempfiles(DATA_DIR)
    if removed:
        print(f"[storage] cleaned orphan tempfiles: {[os.path.basename(p) for p in removed]}")

    # Self-heal: chunks present but index missing → sync rebuild from texts.
    if os.path.exists(TEXTS_PATH) and not os.path.exists(INDEX_PATH):
        with open(TEXTS_PATH, "rb") as f:
            raw = pickle.load(f)
        if raw and isinstance(raw[0], str):
            stored_chunks = [{"text": t, "source": "unknown"} for t in raw]
        else:
            stored_chunks = raw
        chunk_set = set(c["text"] for c in stored_chunks)
        index = faiss.IndexFlatIP(DIM)
        print(f"[index] index.bin missing, rebuilding from {len(stored_chunks)} chunks")
        _rebuild_index_sync()
        rebuild_bm25()
        return

    if os.path.exists(INDEX_PATH) and os.path.exists(TEXTS_PATH):
        global _wal_readonly_reason
        index = faiss.read_index(INDEX_PATH)
        # Dim mismatch degrades to read-only (old index still serves retrieves).
        if index.d != DIM:
            _wal_readonly_reason = (
                f"index dim mismatch: expected={DIM}, actual={index.d} — run /index/rebuild"
            )
            print(f"[index] {_wal_readonly_reason}")
        with open(TEXTS_PATH, "rb") as f:
            raw = pickle.load(f)
        # 向后兼容：旧版存储为 List[str]，迁移为 List[Dict]
        if raw and isinstance(raw[0], str):
            stored_chunks = [{"text": t, "source": "unknown"} for t in raw]
        else:
            stored_chunks = raw
        chunk_set = set(c["text"] for c in stored_chunks)

        # 先做一致性校验：count vs ntotal 不匹配时立即报错，避免后续 reconstruct 崩溃
        if len(stored_chunks) != index.ntotal:
            raise RuntimeError(
                f"storage inconsistency: chunks.count={len(stored_chunks)} != "
                f"index.ntotal={index.ntotal}"
            )

        # Manifest 校验：缺失则自动补齐；存在但与实际不一致则拒绝启动
        manifest = storage.read_manifest(MANIFEST_PATH)
        if manifest is None:
            os.makedirs(STORAGE_DIR, exist_ok=True)
            new_manifest = storage.build_manifest_from_files(
                TEXTS_PATH, INDEX_PATH, len(stored_chunks), index,
                wal_path=os.path.basename(WAL_PATH),
                wal_committed_offset=wal_mod.file_size(WAL_PATH) if WAL_ENABLED else 0,
                wal_committed_seq=0,
            )
            storage.write_manifest(MANIFEST_PATH, new_manifest)
            print(f"[storage] manifest missing, generated at {MANIFEST_PATH}")
            manifest = new_manifest
        else:
            mismatches = storage.verify_manifest(
                manifest, TEXTS_PATH, len(stored_chunks), INDEX_PATH, index,
                wal_path=WAL_PATH if WAL_ENABLED else None,
            )
            if mismatches:
                details = "; ".join(
                    f"{m.field}: expected={m.expected} actual={m.actual}" for m in mismatches
                )
                raise RuntimeError(
                    f"storage manifest mismatch, refusing to start: {details}"
                )

        # WAL replay AFTER manifest check (uses manifest.wal.committed_offset as anchor).
        # Replay may mutate index/stored_chunks, so embedding cache rebuild runs afterward.
        _replay_wal_if_needed(manifest)

        # 从 FAISS 向量直接恢复 embedding 缓存，避免 delete_source 重建索引时重复 encode
        # Dim mismatch 时跳过：reconstruct 的缓冲区与 index.d 不匹配，会损坏内存。
        _emb_cache.clear()
        if index.d == DIM:
            for i, chunk in enumerate(stored_chunks):
                vec = np.zeros(DIM, dtype=np.float32)
                index.reconstruct(i, vec)
                _emb_cache[chunk["text"]] = vec
        # 重建 source → hash 映射（Plan A 跳过相同内容重复入库）
        for chunk in stored_chunks:
            h = chunk.get("source_hash", "")
            if h:
                _source_hashes[chunk.get("source", "unknown")] = h
        print(_t("store_loaded", n=len(stored_chunks)))
    else:
        index = faiss.IndexFlatIP(DIM)
        stored_chunks = []
        chunk_set = set()
        print(_t("store_empty"))
    rebuild_bm25()


def _assert_writable():
    """Raise 503 if service is in WAL-induced read-only mode."""
    if _wal_readonly_reason is not None:
        raise HTTPException(status_code=503, detail=f"storage read-only: {_wal_readonly_reason}")


def _wal_append_op(op: str, payload: Dict) -> tuple:
    """Append one WAL record and return (new_offset, seq). No-op when WAL disabled.

    Caller MUST hold storage.write_lock().
    """
    global _wal_next_seq
    if not WAL_ENABLED:
        return 0, 0
    os.makedirs(STORAGE_DIR, exist_ok=True)
    _wal_next_seq += 1
    record = wal_mod.make_record(seq=_wal_next_seq, op=op, payload=payload)
    new_offset = wal_mod.append(WAL_PATH, wal_mod.encode_record(record))
    return new_offset, _wal_next_seq


def _maybe_checkpoint():
    """Truncate WAL if it has grown past the configured threshold.

    Caller MUST hold storage.write_lock(). Failure logs and leaves WAL intact.
    """
    if not WAL_ENABLED:
        return
    size = wal_mod.file_size(WAL_PATH)
    if size <= WAL_MAX_SIZE_BYTES:
        return
    try:
        save_store(new_index=index, new_chunks=stored_chunks, wal_offset=0, wal_seq=_wal_next_seq)
        wal_mod.truncate_atomic(WAL_PATH)
        print(f"[wal] checkpoint: truncated WAL ({size} bytes) after seq={_wal_next_seq}")
        metrics.last_commit_timestamp_seconds.set(time.time())
        structured_log("checkpoint_done", wal_seq=_wal_next_seq, wal_size_before=size)
    except Exception as e:
        print(f"[wal] checkpoint failed, keeping WAL: {e}")


def save_store(new_index=None, new_chunks=None, *, wal_offset: int = 0, wal_seq: int = 0):
    """Atomically persist the store.

    If new_index / new_chunks are provided, persist those instead of the current
    globals. Caller is responsible for swapping the module globals on success.
    On any failure, existing files on disk are left untouched.

    wal_offset / wal_seq are written into the manifest as the new commit point;
    pass 0 / 0 when WAL is disabled or when checkpointing (post-truncation).
    """
    idx_obj = new_index if new_index is not None else index
    chunks_obj = new_chunks if new_chunks is not None else stored_chunks

    storage.atomic_write_faiss(INDEX_PATH, idx_obj)
    storage.atomic_write_bytes(TEXTS_PATH, pickle.dumps(chunks_obj))

    os.makedirs(STORAGE_DIR, exist_ok=True)
    manifest = storage.build_manifest_from_files(
        TEXTS_PATH, INDEX_PATH, len(chunks_obj), idx_obj,
        wal_path=os.path.basename(WAL_PATH),
        wal_committed_offset=wal_offset,
        wal_committed_seq=wal_seq,
    )
    storage.write_manifest(MANIFEST_PATH, manifest)


# ================= CHUNK =================
def _chunk_plain_text(text: str, min_t: int, max_t: int) -> List[str]:
    """句子级分块（参数化版本），为原 chunk_text 主流程与层次化分块共用。"""
    sentences = re.split(r'(?<=[。！？.!?\n])\s*', text)
    sentences = [s.strip() for s in sentences if s.strip()]

    chunks: List[str] = []
    current: List[str] = []
    current_len = 0

    def flush() -> None:
        nonlocal current, current_len
        chunks.append("".join(current))
        overlap = current[-OVERLAP_SENTENCES:]
        overlap_len = sum(len(s) for s in overlap)
        # overlap 本身超过 max_t 时丢弃，避免下一句立即触发溢出导致重复输出
        if overlap_len >= max_t:
            current = []
            current_len = 0
        else:
            current = overlap
            current_len = overlap_len

    for sentence in sentences:
        # CJK 字符按 1 token/字，其余（英文、数字、空格等）按 4 字符/token 估算
        cjk = sum(1 for c in sentence if '\u4e00' <= c <= '\u9fff')
        est_tokens = cjk + max(1, (len(sentence) - cjk) // 4)

        if current_len + est_tokens > max_t and current:
            flush()

        current.append(sentence)
        current_len += est_tokens

        if current_len >= min_t:
            flush()

    if current:
        chunks.append("".join(current))

    return [c for c in chunks if c.strip()]


def _semantic_encode_fn(texts: List[str]) -> np.ndarray:
    """封装 model.encode 供 semantic_chunker 注入使用，返回归一化 embedding。"""
    return np.asarray(
        model.encode(texts, normalize_embeddings=True, show_progress_bar=False),
        dtype=np.float32,
    )


def _chunk_semantic(text: str, min_t: int, max_t: int) -> List[str]:
    """调用语义分块；异常或未产出结果时回退到句子级分块。"""
    try:
        from semantic_chunker import semantic_chunk
        chunks = semantic_chunk(
            text,
            encode_fn=_semantic_encode_fn,
            threshold_percentile=SEMANTIC_THRESHOLD_PERCENTILE,
            min_chunk_size=SEMANTIC_MIN_CHUNK_SIZE,
            max_chunk_size=SEMANTIC_MAX_CHUNK_SIZE,
            min_tokens=min_t,
            max_tokens=max_t,
        )
        if chunks:
            return chunks
    except Exception as e:
        print(f"[chunk] semantic chunking failed, fallback to plain: {e}")
    return _chunk_plain_text(text, min_t, max_t)


def _chunk_structure(text: str, min_t: int, max_t: int) -> List[str]:
    """结构感知分块：Markdown 走 markdown_chunker，非 Markdown 回退到句子级。"""
    try:
        from markdown_chunker import chunk_markdown, looks_like_markdown
        if looks_like_markdown(text):
            md_chunks = chunk_markdown(
                text, min_t, max_t,
                context_prefix_enabled=CONTEXT_PREFIX_ENABLED,
                context_prefix_max_depth=CONTEXT_PREFIX_MAX_DEPTH,
                context_prefix_format=CONTEXT_PREFIX_FORMAT,
            )
            if md_chunks:
                return md_chunks
    except Exception:
        pass
    return _chunk_plain_text(text, min_t, max_t)


def _build_agentic_llm_fn():
    """构造 agentic 分块用的 LLM 调用函数，复用 llm/ 下的 provider 抽象。

    返回异步函数 ``async (prompt: str) -> str``。
    如果环境未配置 API key、依赖未安装等原因创建 provider 失败，
    返回一个会抛异常的函数，在 agentic_chunk 内部会捕获并降级。
    """
    from llm.factory import get_provider
    llm_cfg = dict(config.get("llm", {}))
    provider = get_provider(llm_cfg)

    async def _llm_fn(prompt: str) -> str:
        return await provider.complete([{"role": "user", "content": prompt}])

    return _llm_fn


def _chunk_agentic(text: str, min_t: int, max_t: int) -> List[str]:
    """Agentic 分块：同步包装。

    FastAPI 中同步 handler 运行于 starlette 的线程池（不占用事件循环），
    因此可安全使用 ``asyncio.run`` 调用异步 LLM；WAL replay 路径也在同步启动阶段，
    同样不冲突。如调用者已在事件循环内使用 agentic，请改调 chunk_text_async。

    失败时降级为结构感知分块（fallback_fn=_chunk_structure）。
    """
    import asyncio
    try:
        from agentic_chunker import agentic_chunk
    except Exception as e:
        print(f"[chunk] agentic module unavailable, fallback to structure: {e}")
        return _chunk_structure(text, min_t, max_t)

    try:
        llm_fn = _build_agentic_llm_fn()
    except Exception as e:
        print(f"[chunk] LLM provider unavailable, fallback to structure: {e}")
        return _chunk_structure(text, min_t, max_t)

    async def _runner() -> List[str]:
        return await agentic_chunk(
            text,
            llm_fn=llm_fn,
            generate_summary=AGENTIC_GENERATE_SUMMARY,
            max_llm_input_tokens=AGENTIC_MAX_LLM_INPUT_TOKENS,
            min_tokens=min_t,
            max_tokens=max_t,
            fallback_fn=_chunk_structure,
        )

    try:
        # 检测是否已在运行中的事件循环中；是的话不能调 asyncio.run
        try:
            asyncio.get_running_loop()
            in_loop = True
        except RuntimeError:
            in_loop = False

        if in_loop:
            # 同步上下文未预期在 loop 内，选择驳回降级避免阻塞
            print("[chunk] _chunk_agentic invoked inside running loop, fallback to structure")
            return _chunk_structure(text, min_t, max_t)
        return asyncio.run(_runner())
    except Exception as e:
        print(f"[chunk] agentic chunking failed, fallback to structure: {e}")
        return _chunk_structure(text, min_t, max_t)


def _chunk_with_size(text: str, min_t: int, max_t: int) -> List[str]:
    """统一分块入口：根据 CHUNK_STRATEGY 路由。

    - strategy=agentic   → LLM 智能分块（同步包装，内部 asyncio.run）
    - strategy=semantic  → 语义分块（与结构感知互斥）
    - strategy=structure → 结构感知分块
    - strategy=fixed     → 保持原有逻辑：structure_aware=True 且为 Markdown 时走结构感知，
                          否则走句子级。这里保持与原代码完全一致的行为，向后兼容。
    """
    if CHUNK_STRATEGY == "agentic":
        return _chunk_agentic(text, min_t, max_t)
    if CHUNK_STRATEGY == "semantic":
        return _chunk_semantic(text, min_t, max_t)
    if CHUNK_STRATEGY == "structure":
        return _chunk_structure(text, min_t, max_t)
    # fixed（默认）：保持原有双条件的分发逻辑
    if STRUCTURE_AWARE_CHUNK:
        try:
            from markdown_chunker import chunk_markdown, looks_like_markdown
            if looks_like_markdown(text):
                md_chunks = chunk_markdown(
                    text, min_t, max_t,
                    context_prefix_enabled=CONTEXT_PREFIX_ENABLED,
                    context_prefix_max_depth=CONTEXT_PREFIX_MAX_DEPTH,
                    context_prefix_format=CONTEXT_PREFIX_FORMAT,
                )
                if md_chunks:
                    return md_chunks
                # 未产出有效 chunk 时回退到原句子级逻辑
        except Exception:
            # 结构感知分块出现异常时不影响主流程，回退到句子级逻辑
            pass
    return _chunk_plain_text(text, min_t, max_t)


def chunk_text(text: str) -> List[str]:
    """原有分块入口：返回平坦的 chunk 字符串列表（使用配置中的 min/max）。"""
    return _chunk_with_size(text, CHUNK_MIN, CHUNK_MAX)


def chunk_text_hierarchical(
    text: str,
    min_tokens: Optional[int] = None,
    max_tokens: Optional[int] = None,
    parent_max_tokens: Optional[int] = None,
) -> List[Dict]:
    """层次化（Parent-Child）分块。

    返回列表，每元素形如 ``{"text": child, "parent_text": parent_or_None, "parent_id": id_or_None}``：
      - 先以 ``parent_max_tokens`` 为上限生成 parent chunks（大块，供 LLM 使用）；
      - 再对每个 parent 按 ``min_tokens``/``max_tokens`` 拆为 child chunks（小块，供 FAISS 检索）；
      - 若 parent 本身太小、拆不出多个 child，child 即为 parent 本身，此时 parent_text=None，避免冷数据冗余。
    """
    if not text or not text.strip():
        return []

    min_t = min_tokens if min_tokens is not None else CHUNK_MIN
    max_t = max_tokens if max_tokens is not None else CHUNK_MAX
    parent_max_t = parent_max_tokens if parent_max_tokens is not None else PARENT_MAX_TOKENS
    # parent 下限设为 parent_max // 2，保证大块尺寸明显大于小块；防止小于 min_t
    parent_min_t = max(min_t, parent_max_t // 2)

    parents = _chunk_with_size(text, parent_min_t, parent_max_t)
    results: List[Dict] = []
    for parent in parents:
        # parent_id 用 parent 文本的 md5 前 12 位：内容不变则 id 稳定，便于检索去重
        parent_id = hashlib.md5(parent.encode("utf-8")).hexdigest()[:12]
        children = _chunk_with_size(parent, min_t, max_t)
        if len(children) <= 1:
            # 边界：parent 本身已在 child 尺寸范围内，无需额外 parent 冷数据
            results.append({"text": parent, "parent_text": None, "parent_id": None})
        else:
            for child in children:
                results.append({"text": child, "parent_text": parent, "parent_id": parent_id})
    return results


# ================= HYBRID SEARCH =================
# 字符级 bigram（连续2字符）：中文词自然包含 bigram，英文也兼容。
# 同时作为 BM25 的 tokenizer，BM25 负责 TF-IDF 加权，比原始覆盖率更准确。
def _bigrams(s: str) -> List[str]:
    s = s.lower()
    return [s[i:i+2] for i in range(len(s) - 1)]


# ================= STARTUP =================
# @app.on_event("startup") 在 FastAPI 0.93+ 已弃用，改用 lifespan 上下文管理器
@asynccontextmanager
async def lifespan(_app: FastAPI):
    load_store()
    if BACKUP_ENABLED:
        os.makedirs(BACKUPS_DIR, exist_ok=True)
        _prune_backups()
        _schedule_next_backup()
    print(_t("service_ready", port=config['server']['port']))
    yield
    # Clean-shutdown checkpoint so the next start has nothing to replay.
    _cancel_backup_timer()
    if WAL_ENABLED and _wal_readonly_reason is None:
        try:
            with storage.write_lock():
                _maybe_checkpoint()
        except Exception as e:
            print(f"[wal] shutdown checkpoint failed: {e}")
    global reranker
    if reranker is not None:
        del reranker
        reranker = None
    import gc
    gc.collect()


# ================= INIT =================
app = FastAPI(title="Local RAG Plugin", lifespan=lifespan)


# ================= API =================
class IngestRequest(BaseModel):
    text: str
    source: str = "unknown"


class RetrieveRequest(BaseModel):
    text: str
    context_tokens_used: int = 0  # 由 hook 传入，用于动态调整 top_k


class RetrieveResponse(BaseModel):
    chunks: List[str]


# ================= INGEST =================
def _ingest_core(text: str, source: str) -> Dict:
    """Core ingest logic callable from both HTTP handler and WAL replay.

    Caller MUST hold storage.write_lock(). Does NOT append WAL — that is the
    handler's job so that replay of a WAL entry doesn't re-append itself.
    """
    global index, stored_chunks, chunk_set

    content_hash = hashlib.md5(text.encode("utf-8")).hexdigest()
    if source in _source_hashes and _source_hashes[source] == content_hash:
        print(_t("ingest_skip", source=source))
        return {"status": "skip", "chunks_added": 0, "reason": "content unchanged"}

    new_chunks = []
    local_chunk_set = set(chunk_set)
    if HIERARCHICAL_ENABLED:
        # 层次化模式：以 child 小块入 FAISS，parent_text 作为元数据随 chunk 存储
        chunk_dicts = chunk_text_hierarchical(text)
        for cd in chunk_dicts:
            ct = cd["text"]
            if ct in local_chunk_set:
                continue
            local_chunk_set.add(ct)
            entry = {"text": ct, "source": source, "source_hash": content_hash}
            # 仅在真有 parent_text 时写入字段，保持与旧 chunk 结构的向后兼容
            if cd.get("parent_text"):
                entry["parent_text"] = cd["parent_text"]
                entry["parent_id"] = cd["parent_id"]
            new_chunks.append(entry)
    else:
        chunks = chunk_text(text)
        for c in chunks:
            if c not in local_chunk_set:
                local_chunk_set.add(c)
                new_chunks.append({"text": c, "source": source, "source_hash": content_hash})

    if not new_chunks:
        _source_hashes[source] = content_hash
        return {"status": "ok", "chunks_added": 0}

    embeddings = encode_with_cache([c["text"] for c in new_chunks])
    new_index = faiss.clone_index(index)
    new_index.add(embeddings)
    merged_chunks = stored_chunks + new_chunks

    # WAL offset + seq are bookkept by the handler before calling us; pass current
    # file size as the new committed offset and current _wal_next_seq.
    save_store(
        new_index=new_index,
        new_chunks=merged_chunks,
        wal_offset=wal_mod.file_size(WAL_PATH) if WAL_ENABLED else 0,
        wal_seq=_wal_next_seq,
    )

    index = new_index
    stored_chunks = merged_chunks
    chunk_set = local_chunk_set
    _source_hashes[source] = content_hash
    rebuild_bm25()
    return {"status": "ok", "chunks_added": len(new_chunks)}


@app.post("/ingest")
def ingest(req: IngestRequest):
    _assert_writable()
    if not req.text.strip():
        metrics.ingest_total.labels(result="error").inc()
        raise HTTPException(status_code=400, detail="text is empty")

    try:
        with storage.write_lock():
            _wal_append_op("ingest", {"text": req.text, "source": req.source})
            result = _ingest_core(req.text, req.source)
            _maybe_checkpoint()
    except HTTPException:
        metrics.ingest_total.labels(result="error").inc()
        raise
    except Exception:
        metrics.ingest_total.labels(result="error").inc()
        raise

    label = "skip" if result.get("status") == "skip" else "ok"
    metrics.ingest_total.labels(result=label).inc()
    metrics.chunk_total.set(len(stored_chunks))
    try:
        metrics.index_bytes.set(os.path.getsize(INDEX_PATH))
    except OSError:
        pass
    structured_log(
        "ingest_done",
        source=req.source,
        status=result.get("status"),
        chunks_added=result.get("chunks_added", 0),
    )
    return result


# ================= RETRIEVE =================
@app.post("/retrieve", response_model=RetrieveResponse)
async def retrieve(req: RetrieveRequest):
    if not req.text.strip():
        raise HTTPException(status_code=400, detail="text is empty")

    t0 = time.perf_counter()

    # Query rewriting: expand/rewrite before vectorisation
    queries: list[str] = [req.text]
    if query_rewrite_enabled:
        _qr_t0 = time.perf_counter()
        try:
            from query_rewrite import rewrite as _qr_rewrite
            queries = await _qr_rewrite(req.text, query_rewrite_strategy)
        except Exception as _qr_err:
            structured_log("query_rewrite_error", {"error": str(_qr_err)})
        finally:
            metrics.query_rewrite_total.labels(strategy=query_rewrite_strategy).inc()
            metrics.query_rewrite_latency_seconds.observe(time.perf_counter() - _qr_t0)

    # If multi_query produced multiple variants, retrieve each and merge
    if len(queries) > 1:
        seen: set[str] = set()
        merged: list[str] = []
        for q in queries:
            sub_req = RetrieveRequest(text=q, context_tokens_used=req.context_tokens_used)
            sub_resp = _retrieve_single(sub_req, t0_override=None)
            for chunk in sub_resp.chunks:
                if chunk not in seen:
                    seen.add(chunk)
                    merged.append(chunk)
        return RetrieveResponse(chunks=merged[:TOP_K])

    return _retrieve_single(req, t0_override=t0)


def _retrieve_single(req: RetrieveRequest, t0_override: Optional[float]) -> RetrieveResponse:
    t0 = t0_override if t0_override is not None else time.perf_counter()

    # 入口一次性 snapshot 全局引用：写路径可能在检索中途原子替换 index/chunks，
    # 使用本地引用保证单次检索全程基于一致快照（GIL 保证单次赋值读原子）
    local_index = index
    local_chunks = stored_chunks
    local_bm25 = bm25

    def log(msg: str):
        if verbose_enabled:
            print(msg)

    q_short = req.text[:60] + ("..." if len(req.text) > 60 else "")
    log(_t("retrieve_query", q=q_short))

    if local_index.ntotal == 0:
        log(_t("retrieve_empty_store"))
        return RetrieveResponse(chunks=[])

    # 动态 top_k：根据已用 token 数计算剩余空间，避免 RAG 结果撑爆上下文窗口
    if dynamic_top_k_enabled and req.context_tokens_used > 0:
        remaining = CONTEXT_WINDOW - req.context_tokens_used - RESPONSE_RESERVE
        chunk_budget = remaining // AVG_CHUNK_TOKENS
        dynamic_top_k = max(1, min(TOP_K, chunk_budget))
        log(_t("retrieve_dynamic_top_k", k=dynamic_top_k, used=req.context_tokens_used, remaining=remaining))
    else:
        dynamic_top_k = TOP_K

    query = f"{QUERY_PREFIX}{req.text}"
    embedding = model.encode([query], normalize_embeddings=True, show_progress_bar=False)
    embedding = np.array(embedding, dtype=np.float32)

    k = min(dynamic_top_k * 3, local_index.ntotal)
    scores, indices = local_index.search(embedding, k)
    log(_t("retrieve_faiss", k=k, n=local_index.ntotal))

    # 先过滤无效候选，再批量计算 BM25（避免逐条调用，性能更好）
    valid_indices: List[int] = []
    valid_vec_scores: List[float] = []
    dropped_threshold = 0
    for score, i in zip(scores[0], indices[0]):
        if i >= len(local_chunks):
            continue
        if score < SCORE_THRESHOLD:
            dropped_threshold += 1
            continue
        valid_indices.append(int(i))
        valid_vec_scores.append(float(score))

    log(_t("retrieve_threshold", t=SCORE_THRESHOLD, d=dropped_threshold, r=len(valid_indices)))

    # BM25 批量评分，归一化到 [0, 1]
    if local_bm25 is not None and valid_indices:
        query_tokens = _bigrams(req.text)
        all_bm25 = local_bm25.get_scores(query_tokens)
        raw_kw = [float(all_bm25[i]) for i in valid_indices]
        max_kw = max(raw_kw) if max(raw_kw) > 0 else 1.0
        kw_scores = [s / max_kw for s in raw_kw]
    else:
        kw_scores = [0.0] * len(valid_indices)

    candidates = []
    for idx, vec_score, kw in zip(valid_indices, valid_vec_scores, kw_scores):
        chunk = local_chunks[idx]
        final_score = vec_score * 0.7 + kw * 0.3
        candidates.append((final_score, chunk, vec_score, kw))

    candidates.sort(key=lambda x: x[0], reverse=True)
    top_candidates = candidates[:dynamic_top_k]

    for final_score, chunk, vec_score, kw in top_candidates:
        src = chunk.get("source", "unknown")
        preview = chunk["text"][:40].replace("\n", " ")
        log(f"  vec={vec_score:.3f} bm25={kw:.3f} final={final_score:.3f} [{src}] {preview!r}")

    if rerank_enabled and reranker is not None and top_candidates:
        pairs = [(req.text, c["text"]) for _, c, _, _ in top_candidates]
        rerank_scores = reranker.predict(pairs, num_workers=0)
        reranked = sorted(zip(rerank_scores, top_candidates), key=lambda x: x[0], reverse=True)
        log(_t("retrieve_rerank_header"))
        for rs, (_, chunk, _, _) in reranked:
            preview = chunk["text"][:40].replace("\n", " ")
            log(f"  rerank={rs:.3f} {preview!r}")
        top_candidates = [t for _, t in reranked]

    log(_t("retrieve_final", n=len(top_candidates)))

    _stats["total_queries"] += 1
    if not top_candidates:
        _stats["zero_hit_queries"] += 1
    _stats["total_chunks_returned"] += len(top_candidates)

    results: List[str] = []
    seen_parent_keys: set = set()
    for _, c, _, _ in top_candidates:
        src = c.get("source", "unknown")
        parent_text = c.get("parent_text")
        parent_id = c.get("parent_id")
        if parent_text:
            # 同一 parent 被多个 child 命中时去重，避免重复占用 LLM 上下文
            key = (src, parent_id)
            if key in seen_parent_keys:
                continue
            seen_parent_keys.add(key)
            results.append(f"[来源: {src}]\n{parent_text}")
        else:
            # 向后兼容：旧 chunk 或未启用层次化时返回 chunk 自身文本
            results.append(f"[来源: {src}]\n{c['text']}")

    latency_s = time.perf_counter() - t0
    metrics.retrieve_latency_seconds.observe(latency_s)
    metrics.retrieve_total.labels(hit="true" if top_candidates else "false").inc()
    structured_log(
        "retrieve_done",
        hit=bool(top_candidates),
        latency_ms=round(latency_s * 1000, 2),
        returned_chunks=len(top_candidates),
    )
    return RetrieveResponse(chunks=results)


# ================= RERANK TOGGLE =================
@app.post("/rerank/toggle")
def rerank_toggle(enabled: bool):
    global rerank_enabled, reranker
    rerank_enabled = enabled
    if enabled and reranker is None:
        print(_t("rerank_loading", model=RERANK_MODEL_NAME))
        reranker = CrossEncoder(RERANK_MODEL_NAME)
        print(_t("rerank_loaded"))
    return {"rerank_enabled": rerank_enabled}


# ================= VERBOSE TOGGLE =================
@app.post("/retrieve/verbose")
def retrieve_verbose(enabled: bool):
    global verbose_enabled
    verbose_enabled = enabled
    return {"verbose_enabled": verbose_enabled}


# ================= DYNAMIC TOP_K TOGGLE =================
@app.post("/retrieve/dynamic-top-k")
def toggle_dynamic_top_k(enabled: bool):
    global dynamic_top_k_enabled
    dynamic_top_k_enabled = enabled
    return {"dynamic_top_k_enabled": dynamic_top_k_enabled}


# ================= CHUNK STRATEGY (RUNTIME SWITCH) =================
class ChunkStrategyRequest(BaseModel):
    strategy: str  # fixed | structure | semantic | agentic


@app.get("/config/chunk-strategy")
def get_chunk_strategy():
    """查询当前分块策略及语义/agentic 分块参数。"""
    return {
        "strategy": CHUNK_STRATEGY,
        "valid": sorted(_VALID_CHUNK_STRATEGIES),
        "structure_aware": STRUCTURE_AWARE_CHUNK,
        "semantic": {
            "threshold_percentile": SEMANTIC_THRESHOLD_PERCENTILE,
            "min_chunk_size": SEMANTIC_MIN_CHUNK_SIZE,
            "max_chunk_size": SEMANTIC_MAX_CHUNK_SIZE,
        },
        "agentic": {
            "generate_summary": AGENTIC_GENERATE_SUMMARY,
            "max_llm_input_tokens": AGENTIC_MAX_LLM_INPUT_TOKENS,
        },
    }


@app.put("/config/chunk-strategy")
def update_chunk_strategy(body: ChunkStrategyRequest):
    """运行时切换分块策略：fixed | structure | semantic | agentic。

    仅影响后续新入库的文档，已入库的 chunk 不会被重新分块。
    """
    global CHUNK_STRATEGY
    s = (body.strategy or "").strip().lower()
    if s not in _VALID_CHUNK_STRATEGIES:
        raise HTTPException(
            status_code=400,
            detail=f"invalid strategy: {body.strategy!r}; valid: {sorted(_VALID_CHUNK_STRATEGIES)}",
        )
    CHUNK_STRATEGY = s
    structured_log("chunk_strategy_changed", strategy=s)
    return {
        "strategy": CHUNK_STRATEGY,
        "note": "仅影响后续入库的文档；已入库 chunk 不变",
    }


# ================= QUERY REWRITE TOGGLE =================
class QueryRewriteToggle(BaseModel):
    enabled: bool
    strategy: Optional[str] = None  # expansion | hyde | multi_query


@app.post("/retrieve/query-rewrite")
def toggle_query_rewrite(body: QueryRewriteToggle):
    global query_rewrite_enabled, query_rewrite_strategy
    query_rewrite_enabled = body.enabled
    if body.strategy:
        query_rewrite_strategy = body.strategy
    return {"query_rewrite_enabled": query_rewrite_enabled, "strategy": query_rewrite_strategy}


# ================= STORAGE INTEGRITY =================
@app.get("/storage/integrity-check")
def storage_integrity_check():
    """Check on-disk files vs manifest vs in-memory index consistency.

    - 200: all consistent (returns summary + committed_at)
    - 200 with regenerated=True: manifest was missing, auto-generated from live state
    - 409: manifest present but mismatches actual files/index
    - 503: pickle or FAISS file unreadable / missing
    """
    if not os.path.exists(INDEX_PATH) or not os.path.exists(TEXTS_PATH):
        raise HTTPException(
            status_code=503,
            detail="storage files missing: chunks.pkl or index.bin not present",
        )

    # Only light sanity checks on live state (no file re-read — globals are authoritative)
    try:
        actual_count = len(stored_chunks)
        actual_ntotal = index.ntotal
        actual_dim = index.d
    except Exception as e:
        raise HTTPException(status_code=503, detail=f"in-memory index unreadable: {e}")

    manifest = storage.read_manifest(MANIFEST_PATH)

    if manifest is None:
        with storage.write_lock():
            # Re-check under lock: another caller may have generated it
            manifest = storage.read_manifest(MANIFEST_PATH)
            if manifest is None:
                os.makedirs(STORAGE_DIR, exist_ok=True)
                manifest = storage.build_manifest_from_files(
                    TEXTS_PATH, INDEX_PATH, actual_count, index
                )
                storage.write_manifest(MANIFEST_PATH, manifest)
        return {
            "status": "ok",
            "regenerated": True,
            "committed_at": manifest.committed_at,
            "chunks": {"count": manifest.chunks.count, "sha256": manifest.chunks.sha256},
            "index": {
                "dim": manifest.index.dim,
                "ntotal": manifest.index.ntotal,
                "sha256": manifest.index.sha256,
            },
            "wal": {
                "committed_offset": manifest.wal.committed_offset,
                "committed_seq": manifest.wal.committed_seq,
            },
            "disk_free_bytes": _safe_disk_free(),
        }

    try:
        mismatches = storage.verify_manifest(
            manifest, TEXTS_PATH, actual_count, INDEX_PATH, index
        )
    except OSError as e:
        raise HTTPException(status_code=503, detail=f"storage file unreadable: {e}")

    if mismatches:
        return JSONResponse(
            status_code=409,
            content={
                "status": "mismatch",
                "committed_at": manifest.committed_at,
                "mismatches": [m.to_dict() for m in mismatches],
            },
        )

    return {
        "status": "ok",
        "regenerated": False,
        "committed_at": manifest.committed_at,
        "chunks": {"count": manifest.chunks.count, "sha256": manifest.chunks.sha256},
        "index": {
            "dim": manifest.index.dim,
            "ntotal": manifest.index.ntotal,
            "sha256": manifest.index.sha256,
        },
        "wal": {
            "committed_offset": manifest.wal.committed_offset,
            "committed_seq": manifest.wal.committed_seq,
        },
        "disk_free_bytes": _safe_disk_free(),
    }


# ================= HEALTH =================
@app.get("/health")
def health():
    try:
        free_bytes = shutil.disk_usage(DATA_DIR).free
    except OSError:
        free_bytes = -1  # treat unreadable disk as ok-unknown, not error

    if free_bytes >= 0 and free_bytes < DISK_FREE_ERROR_BYTES:
        status_value = "error"
        reason = f"disk free {free_bytes} bytes below threshold {DISK_FREE_ERROR_BYTES}"
        http_code = 503
    elif _index_rebuilding:
        status_value = "degraded"
        reason = "index rebuild in progress"
        http_code = 200
    elif _wal_readonly_reason is not None:
        status_value = "degraded"
        reason = _wal_readonly_reason
        http_code = 200
    elif _wal_replaying:
        status_value = "degraded"
        reason = "wal replay in progress"
        http_code = 200
    else:
        status_value = "ok"
        reason = None
        http_code = 200

    if _index_rebuilding:
        index_state = "rebuilding"
    elif _wal_readonly_reason is not None:
        index_state = "read-only"
    else:
        index_state = "normal"

    body = {
        "status": status_value,
        "reason": reason,
        "total_chunks": len(stored_chunks),
        "rerank_enabled": rerank_enabled,
        "verbose_enabled": verbose_enabled,
        "wal_replaying": _wal_replaying,
        "wal_readonly_reason": _wal_readonly_reason,
        "disk_free_bytes": free_bytes,
        "index_rebuilding": _index_rebuilding,
        "index_state": index_state,
    }
    if http_code == 200:
        return body
    return JSONResponse(status_code=http_code, content=body)


@app.get("/metrics")
def metrics_endpoint():
    from fastapi.responses import Response
    return Response(content=metrics.render(), media_type=metrics.content_type)


@app.post("/backup/run")
def backup_run_endpoint():
    _assert_writable()
    with storage.write_lock():
        result = _backup_run_core()
        _prune_backups()
    return result


@app.get("/backup/list")
def backup_list_endpoint():
    return backup.list_backups(BACKUPS_DIR)


class RestoreRequest(BaseModel):
    file: str
    confirm: bool = False


@app.post("/backup/restore")
def backup_restore_endpoint(req: RestoreRequest):
    if not req.confirm:
        raise HTTPException(status_code=400, detail="confirm=true required for destructive restore")
    return _restore_core(req.file)


@app.post("/index/rebuild")
def index_rebuild_endpoint():
    global _index_rebuilding, _wal_readonly_reason, _index_rebuild_progress
    import threading as _threading
    if _index_rebuilding:
        raise HTTPException(status_code=409, detail="rebuild already in progress")
    _index_rebuilding = True
    _index_rebuild_progress = 0.0
    _wal_readonly_reason = "index rebuild in progress"
    t = _threading.Thread(target=_rebuild_index_async, daemon=True)
    t.start()
    return {"status": "started", "total_chunks": len(stored_chunks)}


@app.get("/index/status")
def index_status_endpoint():
    if _index_rebuilding:
        return {
            "state": "rebuilding",
            "progress_ratio": _index_rebuild_progress,
            "reason": "index rebuild in progress",
        }
    if _wal_readonly_reason is not None:
        return {"state": "read-only", "reason": _wal_readonly_reason}
    return {"state": "normal"}


# ================= STATS =================
@app.get("/stats")
def stats():
    total = _stats["total_queries"]
    zero = _stats["zero_hit_queries"]
    returned = _stats["total_chunks_returned"]
    hit_rate = round((total - zero) / total * 100, 1) if total > 0 else None
    avg_chunks = round(returned / total, 2) if total > 0 else None
    return {
        "total_queries": total,
        "zero_hit_queries": zero,
        "hit_rate_pct": hit_rate,
        "avg_chunks_per_query": avg_chunks,
        "note": "重启服务后统计重置"
    }


# ================= SOURCES =================
# 列出所有已入库的来源及各来源 chunk 数，便于管理和溯源。
@app.get("/sources")
def sources():
    counter: Dict[str, int] = {}
    for c in stored_chunks:
        src = c.get("source", "unknown")
        counter[src] = counter.get(src, 0) + 1
    return {"sources": [{"name": k, "chunks": v} for k, v in sorted(counter.items())]}


# ================= DELETE BY SOURCE =================
# 按来源名称删除 chunks。
# FAISS IndexFlatIP 不支持按 id 删除，必须用剩余 chunks 重建整个索引。
def _delete_source_core(name: str) -> Dict:
    """Core delete-source logic callable from both HTTP handler and replay.
    Caller MUST hold storage.write_lock()."""
    global index, stored_chunks, chunk_set

    remaining = [c for c in stored_chunks if c.get("source") != name]
    removed = len(stored_chunks) - len(remaining)
    if removed == 0:
        raise HTTPException(status_code=404, detail=f"source '{name}' not found")

    new_index = faiss.IndexFlatIP(DIM)
    if remaining:
        embeddings = encode_with_cache([c["text"] for c in remaining])
        new_index.add(embeddings)

    save_store(
        new_index=new_index,
        new_chunks=remaining,
        wal_offset=wal_mod.file_size(WAL_PATH) if WAL_ENABLED else 0,
        wal_seq=_wal_next_seq,
    )

    index = new_index
    stored_chunks = remaining
    chunk_set = set(c["text"] for c in remaining)
    for k in [k for k in list(_emb_cache.keys()) if k not in chunk_set]:
        del _emb_cache[k]
    _source_hashes.pop(name, None)
    rebuild_bm25()
    return {"status": "ok", "removed_chunks": removed}


@app.delete("/source")
def delete_source(name: str):
    _assert_writable()
    with storage.write_lock():
        _wal_append_op("delete_source", {"name": name})
        result = _delete_source_core(name)
        _maybe_checkpoint()
        return result


# ================= RESET =================
def _reset_core() -> Dict:
    """Core reset logic callable from both HTTP handler and replay.
    Caller MUST hold storage.write_lock()."""
    global index, stored_chunks, chunk_set

    new_index = faiss.IndexFlatIP(DIM)
    save_store(
        new_index=new_index,
        new_chunks=[],
        wal_offset=wal_mod.file_size(WAL_PATH) if WAL_ENABLED else 0,
        wal_seq=_wal_next_seq,
    )

    index = new_index
    stored_chunks = []
    chunk_set = set()
    _emb_cache.clear()
    _source_hashes.clear()
    rebuild_bm25()
    return {"status": "reset"}


@app.delete("/reset")
def reset():
    _assert_writable()
    with storage.write_lock():
        _wal_append_op("reset", {})
        result = _reset_core()
        _maybe_checkpoint()
        return result


# ================= EXPORT =================
@app.get("/export")
def export():
    if not os.path.exists(INDEX_PATH) or not os.path.exists(TEXTS_PATH):
        raise HTTPException(status_code=404, detail="向量库为空，无数据可导出")
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        zf.write(INDEX_PATH, "index.bin")
        zf.write(TEXTS_PATH, "chunks.pkl")
    buf.seek(0)
    return StreamingResponse(
        buf,
        media_type="application/zip",
        headers={"Content-Disposition": "attachment; filename=rag_backup.zip"},
    )


# ================= IMPORT =================
@app.post("/import")
async def import_kb(file: UploadFile = File(...)):
    content = await file.read()
    buf = io.BytesIO(content)
    try:
        with zipfile.ZipFile(buf, "r") as zf:
            if "index.bin" not in zf.namelist() or "chunks.pkl" not in zf.namelist():
                raise HTTPException(status_code=400, detail="无效备份：缺少 index.bin 或 chunks.pkl")
            tmp_index = INDEX_PATH + ".tmp"
            tmp_texts = TEXTS_PATH + ".tmp"
            with open(tmp_index, "wb") as f:
                f.write(zf.read("index.bin"))
            with open(tmp_texts, "wb") as f:
                f.write(zf.read("chunks.pkl"))
    except zipfile.BadZipFile:
        raise HTTPException(status_code=400, detail="不是有效的 zip 文件")
    shutil.move(tmp_index, INDEX_PATH)
    shutil.move(tmp_texts, TEXTS_PATH)
    load_store()
    return {"status": "ok", "chunks_imported": len(stored_chunks)}


# ================= AGENT =================
from agent.loop import run as _agent_run
from agent import memory as _agent_memory


class AgentChatRequest(BaseModel):
    message: str
    session_id: Optional[str] = None


@app.post("/agent/chat")
async def agent_chat(body: AgentChatRequest):
    session_id = body.session_id or _agent_memory.new_session()
    reply = await _agent_run(session_id, body.message)
    return {"session_id": session_id, "reply": reply}


@app.post("/agent/session")
async def agent_create_session():
    sid = _agent_memory.new_session()
    return {"session_id": sid}


@app.get("/agent/sessions")
async def agent_list_sessions():
    return {"sessions": _agent_memory.list_sessions()}


@app.delete("/agent/session/{session_id}")
async def agent_delete_session(session_id: str):
    _agent_memory.delete_session(session_id)
    return {"deleted": session_id}

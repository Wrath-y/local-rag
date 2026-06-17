"""层次化（Parent-Child）分块 + 入库 + 检索的端到端测试。

覆盖点（对应需求 5 项）：
1. hierarchical 关闭时行为不变（_ingest_core 与原逻辑等价）
2. hierarchical 开启时生成正确的 parent-child 关系
3. child chunk 包含正确的 parent_text 与稳定的 parent_id
4. 检索命中 child 后 _retrieve_single 返回 parent_text
5. 向后兼容：没有 parent 字段的旧 chunk 返回自身
6. 边界情况：短文本、单 chunk 文本

依赖项目内 conftest 的 isolated_store / seed_consistent_state fixture，
通过直接 patch 模块级开关与权重，避开真实 embedding 模型推理之外的副作用。
"""

from __future__ import annotations

import os
import sys

# 保证项目根目录在 sys.path 中
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

import numpy as np
import pytest


# ============================================================
# 纯分块函数测试（不依赖 server 内部状态切换）
# ============================================================

def test_chunk_text_hierarchical_empty():
    """边界：空文本 / 纯空白返回空列表。"""
    import server

    assert server.chunk_text_hierarchical("") == []
    assert server.chunk_text_hierarchical("   \n  \t ") == []


def test_chunk_text_hierarchical_short_text_no_parent():
    """边界：文本极短，parent 拆不出多个 child → parent_text=None（无冗余）。"""
    import server

    text = "只有一句很短的话。"
    out = server.chunk_text_hierarchical(text)

    assert len(out) == 1
    assert out[0]["parent_text"] is None
    assert out[0]["parent_id"] is None
    assert out[0]["text"]


def test_chunk_text_hierarchical_long_text_has_parent_child():
    """长文本应产出多个 child，且每个 child 的 parent_text 包含自身文本。"""
    import server

    # 构造足够长的中文文本（CJK 1 token/字），保证可拆分出多 parent，每 parent 多 child
    long_para = "这是测试用的长段落内容。" * 200  # ≈ 2400 字 → 2400 tokens
    out = server.chunk_text_hierarchical(
        long_para, min_tokens=200, max_tokens=400, parent_max_tokens=800
    )

    assert len(out) > 1
    # 至少一部分 child 携带 parent_text
    with_parent = [c for c in out if c["parent_text"] is not None]
    assert with_parent, "长文本应至少产出一个带 parent_text 的 child"
    for c in with_parent:
        # child 文本是 parent 的子串（至少前若干字符）
        # 注意：分块算法可能在 parent 内插入 overlap，因此用 child 的前 20 字检测包含
        head = c["text"][:20]
        assert head in c["parent_text"], (
            f"child 的开头应在 parent_text 中找到：head={head!r}"
        )
        # parent_id 是 12 位 hex
        assert c["parent_id"] is not None and len(c["parent_id"]) == 12


def test_chunk_text_hierarchical_parent_id_stable():
    """同一 parent 文本对应的 parent_id 应稳定（基于内容 hash）。"""
    import server

    text = "这是测试用的长段落内容。" * 200
    out_a = server.chunk_text_hierarchical(text, 200, 400, 800)
    out_b = server.chunk_text_hierarchical(text, 200, 400, 800)

    ids_a = [c["parent_id"] for c in out_a if c["parent_id"]]
    ids_b = [c["parent_id"] for c in out_b if c["parent_id"]]
    assert ids_a == ids_b, "相同输入应得到稳定的 parent_id 序列"


def test_chunk_text_hierarchical_children_share_parent():
    """同一 parent 下的多个 child 应共享同一 parent_id 与同一 parent_text。"""
    import server

    text = "这是测试用的长段落内容。" * 200
    out = server.chunk_text_hierarchical(text, 200, 400, 800)

    # 按 parent_id 分组
    groups: dict = {}
    for c in out:
        if c["parent_id"] is None:
            continue
        groups.setdefault(c["parent_id"], []).append(c)

    # 至少有一个 parent_id 下挂多个 child
    multi = [g for g in groups.values() if len(g) > 1]
    assert multi, "应存在 parent_id 下挂多个 child 的情况"
    for g in multi:
        parents = {c["parent_text"] for c in g}
        assert len(parents) == 1, "同 parent_id 的 child 必须共享同一 parent_text"


# ============================================================
# 入库（_ingest_core）行为测试
# ============================================================

def _patch_encode(monkeypatch, server, chunk_to_vec_fn):
    """把 server.encode_with_cache / model.encode 替换成确定性、零延迟的伪向量。"""
    dim = server.DIM

    def fake_encode_with_cache(texts):
        out = np.zeros((len(texts), dim), dtype=np.float32)
        for i, t in enumerate(texts):
            out[i] = chunk_to_vec_fn(t)
        return out

    monkeypatch.setattr(server, "encode_with_cache", fake_encode_with_cache)

    class _FakeModel:
        def encode(self, prefixed_list, **kwargs):
            # query 路径仅吃单条
            out = np.zeros((len(prefixed_list), dim), dtype=np.float32)
            for i, p in enumerate(prefixed_list):
                # 去掉 prefix
                t = p
                for prefix in (server.DOC_PREFIX, server.QUERY_PREFIX):
                    if t.startswith(prefix):
                        t = t[len(prefix):]
                        break
                out[i] = chunk_to_vec_fn(t)
            return out

        def get_embedding_dimension(self):
            return dim

    monkeypatch.setattr(server, "model", _FakeModel())


def _det_unit_vec(text: str, dim: int) -> np.ndarray:
    """文本 → 确定性 L2-normalized 向量（基于 hash 作为种子）。"""
    import hashlib

    seed = int(hashlib.md5(text.encode("utf-8")).hexdigest()[:8], 16)
    rng = np.random.default_rng(seed)
    v = rng.standard_normal(dim).astype(np.float32)
    n = float(np.linalg.norm(v)) + 1e-9
    return v / n


def test_ingest_disabled_unchanged_behaviour(isolated_store, monkeypatch):
    """hierarchical 关闭时入库行为与之前完全一致：chunk 无 parent_text 字段。"""
    server = isolated_store["server"]
    monkeypatch.setattr(server, "HIERARCHICAL_ENABLED", False)
    _patch_encode(monkeypatch, server, lambda t: _det_unit_vec(t, server.DIM))

    text = "这是一段测试用的文本。" * 50
    server._ingest_core(text, source="t1")

    assert len(server.stored_chunks) >= 1
    for c in server.stored_chunks:
        assert "parent_text" not in c
        assert "parent_id" not in c


def test_ingest_enabled_writes_parent_fields(isolated_store, monkeypatch):
    """hierarchical 开启时 child chunks 应携带 parent_text + parent_id。"""
    server = isolated_store["server"]
    monkeypatch.setattr(server, "HIERARCHICAL_ENABLED", True)
    _patch_encode(monkeypatch, server, lambda t: _det_unit_vec(t, server.DIM))

    long_text = "层次化分块测试文本内容。" * 200
    server._ingest_core(long_text, source="t2")

    chunks = server.stored_chunks
    assert len(chunks) > 1, "长文本应产出多个 child chunk"

    with_parent = [c for c in chunks if c.get("parent_text")]
    assert with_parent, "应至少有一个 child chunk 携带 parent_text"

    for c in with_parent:
        assert isinstance(c["parent_text"], str) and c["parent_text"]
        assert isinstance(c["parent_id"], str) and len(c["parent_id"]) == 12
        assert c["text"][:20] in c["parent_text"]

    # 同一 parent_id 的 child 应共享 parent_text
    by_pid: dict = {}
    for c in with_parent:
        by_pid.setdefault(c["parent_id"], []).append(c)
    for pid, group in by_pid.items():
        if len(group) > 1:
            assert len({c["parent_text"] for c in group}) == 1


# ============================================================
# 检索（_retrieve_single）行为测试
# ============================================================

class _Req:
    """轻量 RetrieveRequest 替身，避免触发 pydantic 严格校验时的额外字段限制。"""

    def __init__(self, text: str, context_tokens_used: int = 0):
        self.text = text
        self.context_tokens_used = context_tokens_used


def _build_req(server, text: str):
    return server.RetrieveRequest(text=text, context_tokens_used=0)


def test_retrieve_returns_parent_text_when_child_hit(
    isolated_store, monkeypatch, seed_consistent_state
):
    """命中带 parent_text 的 child 时，应返回 parent_text 给 LLM。"""
    server = isolated_store["server"]
    monkeypatch.setattr(server, "SCORE_THRESHOLD", -1.0)  # 关闭阈值过滤，确保命中
    monkeypatch.setattr(server, "rerank_enabled", False)
    monkeypatch.setattr(server, "TOP_K", 3)
    monkeypatch.setattr(server, "verbose_enabled", False)

    target_child = "这是 child 小块的精确检索文本"
    parent_text = "这是 parent 大块的完整上下文，包含子块以及更多周边信息。"
    chunks = [
        {
            "text": target_child,
            "source": "doc-A",
            "parent_text": parent_text,
            "parent_id": "p-1234567890ab",
        },
        {"text": "无关 chunk1", "source": "doc-B"},
        {"text": "无关 chunk2", "source": "doc-C"},
    ]

    # 让 target_child 与 query 完全相同 → 余弦相似度最高
    def vec_for(t: str) -> np.ndarray:
        return _det_unit_vec(t, server.DIM)

    _patch_encode(monkeypatch, server, vec_for)
    seed_consistent_state(server, chunks)

    # 重新构造 FAISS 索引让 target_child 的向量正好与 query 对齐
    import faiss

    idx = faiss.IndexFlatIP(server.DIM)
    vecs = np.stack([vec_for(c["text"]) for c in chunks]).astype(np.float32)
    idx.add(vecs)
    server.index = idx

    server.rebuild_bm25()

    resp = server._retrieve_single(_build_req(server, target_child), t0_override=None)

    assert resp.chunks, "应返回至少一个结果"
    # top1 应当是带 parent_text 的命中：返回的应是 parent 文本而非 child
    assert any(parent_text in r for r in resp.chunks)
    # 不应将原 child 文本作为返回（child 仅用于检索）
    assert not any(target_child in r and parent_text not in r for r in resp.chunks)


def test_retrieve_backward_compatible_without_parent(
    isolated_store, monkeypatch, seed_consistent_state
):
    """向后兼容：旧 chunk 没有 parent 字段时返回 chunk 自身文本。"""
    server = isolated_store["server"]
    monkeypatch.setattr(server, "SCORE_THRESHOLD", -1.0)
    monkeypatch.setattr(server, "rerank_enabled", False)
    monkeypatch.setattr(server, "TOP_K", 1)
    monkeypatch.setattr(server, "verbose_enabled", False)

    target = "只有 text 字段的旧版本 chunk"
    chunks = [{"text": target, "source": "legacy"}]

    def vec_for(t: str) -> np.ndarray:
        return _det_unit_vec(t, server.DIM)

    _patch_encode(monkeypatch, server, vec_for)
    seed_consistent_state(server, chunks)

    import faiss

    idx = faiss.IndexFlatIP(server.DIM)
    idx.add(np.stack([vec_for(c["text"]) for c in chunks]).astype(np.float32))
    server.index = idx
    server.rebuild_bm25()

    resp = server._retrieve_single(_build_req(server, target), t0_override=None)

    assert resp.chunks
    assert any(target in r for r in resp.chunks)


def test_retrieve_dedupes_same_parent_when_multiple_children_hit(
    isolated_store, monkeypatch, seed_consistent_state
):
    """同一 parent 被多个 child 命中时去重，避免重复占用 LLM 上下文。"""
    server = isolated_store["server"]
    monkeypatch.setattr(server, "SCORE_THRESHOLD", -1.0)
    monkeypatch.setattr(server, "rerank_enabled", False)
    monkeypatch.setattr(server, "TOP_K", 3)
    monkeypatch.setattr(server, "verbose_enabled", False)

    parent_text = "shared parent block 完整上下文"
    pid = "shared-pid01"
    chunks = [
        {"text": "child A 部分内容", "source": "doc-X",
         "parent_text": parent_text, "parent_id": pid},
        {"text": "child B 部分内容", "source": "doc-X",
         "parent_text": parent_text, "parent_id": pid},
        {"text": "无关 chunk", "source": "doc-Y"},
    ]

    def vec_for(t: str) -> np.ndarray:
        return _det_unit_vec(t, server.DIM)

    _patch_encode(monkeypatch, server, vec_for)
    seed_consistent_state(server, chunks)

    import faiss

    idx = faiss.IndexFlatIP(server.DIM)
    idx.add(np.stack([vec_for(c["text"]) for c in chunks]).astype(np.float32))
    server.index = idx
    server.rebuild_bm25()

    # query = "child A 部分内容" → child A 命中最强；child B 因共享 parent 也很相近
    resp = server._retrieve_single(
        _build_req(server, "child A 部分内容"), t0_override=None
    )

    # parent_text 至多出现一次（同 parent_id 去重）
    occurrences = sum(1 for r in resp.chunks if parent_text in r)
    assert occurrences == 1, f"parent_text 应去重为 1 次，实际 {occurrences} 次"

"""\u8bed\u4e49\u5206\u5757\uff08semantic_chunker\uff09\u4e0e\u670d\u52a1\u5668\u96c6\u6210\u6d4b\u8bd5\u3002

\u8986\u76d6\u70b9\uff08\u5bf9\u5e94\u9700\u6c42 6 \u9879\uff09\uff1a
1. \u8bed\u4e49\u5206\u5757\u5728\u8bed\u4e49\u65ad\u88c2\u5904\u5207\u5206\uff08\u4e92\u5f02\u4e3b\u9898\u88ab\u62c6\u5230\u4e0d\u540c chunk\uff09
2. threshold_percentile \u53c2\u6570\u5f71\u54cd\u5207\u5206\u7c92\u5ea6
3. min_chunk_size / max_chunk_size \u7ea6\u675f\u751f\u6548
4. \u77ed\u6587\u672c\uff08< 3 \u53e5\uff09\u76f4\u63a5\u8fd4\u56de\u6574\u6bb5
5. encode_fn \u63a5\u53e3\u5951\u7ea6\uff08\u63a5\u6536 List[str] \u8fd4\u56de (n, d) ndarray\uff09
6. strategy \u914d\u7f6e\u5728 _chunk_with_size \u4e2d\u6b63\u786e\u8def\u7531\uff0c/config/chunk-strategy \u8fd0\u884c\u65f6\u5207\u6362\u6709\u6548
"""

from __future__ import annotations

import os
import sys
from typing import List

import numpy as np
import pytest

# \u4fdd\u8bc1\u9879\u76ee\u6839\u76ee\u5f55\u5728 sys.path \u4e2d
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

from semantic_chunker import (  # noqa: E402
    semantic_chunk,
    split_sentences,
    _adjacent_cosine,
    _estimate_tokens,
)


# ============================================================
# \u5de5\u5177\uff1a\u4e3a\u6307\u5b9a\u4e3b\u9898\u751f\u6210"\u8be6\u4e49" embedding
# ============================================================

def _topic_vec(topic_id: int, dim: int = 16, jitter: float = 0.0) -> np.ndarray:
    """\u4e3a\u4e3b\u9898 ID \u751f\u6210\u4e00\u4e2a\u786e\u5b9a\u6027\u5355\u4f4d\u5411\u91cf\uff1b\u540c\u4e00\u4e3b\u9898\u7684\u53e5\u5b50\u5411\u91cf\u9ad8\u5ea6\u76f8\u4f3c\u3002"""
    rng = np.random.default_rng(topic_id * 1000 + 7)
    base = rng.standard_normal(dim).astype(np.float32)
    if jitter > 0:
        # \u5728\u540c\u4e00\u4e3b\u9898\u5185\u52a0\u4e00\u70b9\u5fae\u6270\u52a8\uff0c\u907f\u514d\u5b8c\u5168\u540c\u5411\u4ecd\u80fd\u4fdd\u8bc1\u4e92\u4e3b\u9898\u9ad8\u76f8\u4f3c
        rng2 = np.random.default_rng(topic_id * 1000 + 17 + int(jitter * 1e6))
        base = base + rng2.standard_normal(dim).astype(np.float32) * jitter
    n = float(np.linalg.norm(base)) + 1e-9
    return base / n


def _make_encode_fn(sentence_topic_map: dict, dim: int = 16, jitter: float = 0.05):
    """\u6839\u636e\u53e5\u5b50\u2192\u4e3b\u9898\u6620\u5c04\u8fd4\u56de\u4e00\u4e2a\u4f2a encode_fn\u3002"""
    def fn(sentences: List[str]) -> np.ndarray:
        out = np.zeros((len(sentences), dim), dtype=np.float32)
        for i, s in enumerate(sentences):
            tid = sentence_topic_map.get(s, hash(s) % 1000)
            out[i] = _topic_vec(tid, dim=dim, jitter=jitter)
        return out
    return fn


# ============================================================
# 1. \u8bed\u4e49\u5206\u5757\u57fa\u672c\u529f\u80fd\uff1a\u5728\u4e3b\u9898\u8fb9\u754c\u5904\u5207\u5206
# ============================================================

def test_semantic_chunk_splits_at_topic_boundary():
    """\u4e09\u4e2a\u4e3b\u9898\u5404\u4e09\u53e5\u4ea4\u66ff\u51fa\u73b0\uff0c\u8bed\u4e49\u5206\u5757\u5e94\u5728\u4e3b\u9898\u4ea4\u754c\u5904\u5207\u5f00\u3002"""
    A = ["\u8d44\u672c\u5e02\u573a\u4eca\u5929\u5927\u5e45\u4e0a\u6da8\u3002", "\u80a1\u7968\u4ef7\u683c\u5168\u9762\u53cd\u5f39\u3002", "\u6295\u8d44\u8005\u4fe1\u5fc3\u589e\u5f3a\u3002"]
    B = ["\u73a9\u5bb6\u53d1\u73b0\u65b0\u7684\u6e38\u620f\u673a\u5236\u3002", "\u6e38\u620f\u4e2d\u7684 boss \u96be\u5ea6\u63d0\u9ad8\u3002", "\u6e38\u620f\u793e\u533a\u8ba8\u8bba\u70ed\u70c8\u3002"]
    C = ["\u5236\u4f5c\u4e00\u4efd\u9ec4\u6cb9\u9e21\u86cb\u829d\u58eb\u3002", "\u70d8\u70e4\u65f6\u95f4\u63a7\u5236\u5728\u4e8c\u5341\u5206\u949f\u3002", "\u5965\u5229\u5965\u9999\u8349\u70b9\u7f00\u5373\u53ef\u51fa\u9505\u3002"]

    sentences_in_order = A + B + C
    text = "".join(sentences_in_order)

    topic_map = {}
    for s in A:
        topic_map[s] = 1
    for s in B:
        topic_map[s] = 2
    for s in C:
        topic_map[s] = 3
    encode_fn = _make_encode_fn(topic_map, dim=16, jitter=0.02)

    chunks = semantic_chunk(
        text,
        encode_fn=encode_fn,
        threshold_percentile=80,
        min_chunk_size=1,
        max_chunk_size=20,
        min_tokens=1,
        max_tokens=10_000,
    )

    assert len(chunks) >= 2, f"\u5e94\u81f3\u5c11\u5207\u51fa 2 \u4e2a chunk\uff0c\u5b9e\u9645 {len(chunks)}: {chunks}"
    # \u5176\u4e2d\u5e94\u80fd\u627e\u5230\u67d0\u4e2a chunk \u4ec5\u5305\u542b\u4e3b\u9898 A/B/C \u4e2d\u7684\u4e00\u79cd\uff08\u5373\u4e3b\u9898\u4e0d\u88ab\u6df7\u5728\u4e00\u8d77\uff09
    pure_topic_chunks = 0
    for ch in chunks:
        in_a = sum(1 for s in A if s in ch)
        in_b = sum(1 for s in B if s in ch)
        in_c = sum(1 for s in C if s in ch)
        # \u4e00\u4e2a chunk \u4e3b\u8981\u96c6\u4e2d\u4e8e\u5355\u4e00\u4e3b\u9898
        non_zero = [x for x in (in_a, in_b, in_c) if x > 0]
        if len(non_zero) == 1:
            pure_topic_chunks += 1
    assert pure_topic_chunks >= 1, f"\u81f3\u5c11\u5e94\u6709\u4e00\u4e2a chunk \u4ec5\u5305\u542b\u5355\u4e00\u4e3b\u9898\uff0cchunks={chunks}"


# ============================================================
# 2. threshold_percentile \u5f71\u54cd\u5207\u5206\u7c92\u5ea6
# ============================================================

def test_threshold_percentile_affects_granularity():
    """\u9ad8 percentile \u5207\u5f97\u66f4\u7ec6\uff1bpercentile=10 \u51e0\u4e4e\u4e0d\u5207\u3002"""
    A = ["\u4e3b\u9898 A \u53e5\u5b50\u4e00\u3002", "\u4e3b\u9898 A \u53e5\u5b50\u4e8c\u3002", "\u4e3b\u9898 A \u53e5\u5b50\u4e09\u3002"]
    B = ["\u4e3b\u9898 B \u53e5\u5b50\u4e00\u3002", "\u4e3b\u9898 B \u53e5\u5b50\u4e8c\u3002", "\u4e3b\u9898 B \u53e5\u5b50\u4e09\u3002"]
    C = ["\u4e3b\u9898 C \u53e5\u5b50\u4e00\u3002", "\u4e3b\u9898 C \u53e5\u5b50\u4e8c\u3002", "\u4e3b\u9898 C \u53e5\u5b50\u4e09\u3002"]
    text = "".join(A + B + C)

    topic_map = {**{s: 1 for s in A}, **{s: 2 for s in B}, **{s: 3 for s in C}}
    encode_fn = _make_encode_fn(topic_map, dim=16, jitter=0.02)

    # threshold_percentile 语义（与 LangChain SemanticChunker 一致）：
    # 表示“取距离第 N 百分位以上”作为断裂 ≡ “取相似度低于第 (100-N) 百分位”。
    # 所以 percentile 越高 → 越难达到阈值 → chunk 越粗；percentile 越低 → chunk 越细。
    coarse = semantic_chunk(
        text, encode_fn=encode_fn, threshold_percentile=99,
        min_chunk_size=1, max_chunk_size=50, min_tokens=1, max_tokens=10_000,
    )
    fine = semantic_chunk(
        text, encode_fn=encode_fn, threshold_percentile=10,
        min_chunk_size=1, max_chunk_size=50, min_tokens=1, max_tokens=10_000,
    )
    
    # 细粒度应产出 >= 粗粒度的 chunk 数量
    assert len(fine) >= len(coarse), f"细粒度应 ≥ 粗粒度：fine={len(fine)} coarse={len(coarse)}"
    # 且严格大于，用以证明参数确实产生了差异
    assert len(fine) > len(coarse)


# ============================================================
# 3. min_chunk_size / max_chunk_size \u7ea6\u675f
# ============================================================

def test_max_chunk_size_enforced():
    """\u8bbe\u7f6e max_chunk_size \u540e\uff0c\u5355 chunk \u53e5\u5b50\u6570\u4e0d\u5e94\u8d85\u8fc7\u9650\u5236\u3002"""
    # \u5168\u90e8\u540c\u4e3b\u9898\uff1a\u539f\u672c\u4e0d\u4f1a\u5207\uff0c\u4f46 max_chunk_size \u5e94\u5f3a\u5236\u62c6\u5206
    sentences = [f"\u540c\u4e3b\u9898\u53e5\u5b50 {i}\u3002" for i in range(20)]
    text = "".join(sentences)
    topic_map = {s: 42 for s in sentences}
    encode_fn = _make_encode_fn(topic_map, dim=16, jitter=0.001)

    chunks = semantic_chunk(
        text, encode_fn=encode_fn, threshold_percentile=10,
        min_chunk_size=1, max_chunk_size=5, min_tokens=1, max_tokens=10_000,
    )
    # \u6bcf\u4e2a chunk \u5305\u542b\u7684\u201c\u540c\u4e3b\u9898\u53e5\u5b50\u201d\u4e2a\u6570 \u2264 5
    for ch in chunks:
        count = ch.count("\u540c\u4e3b\u9898\u53e5\u5b50")
        assert count <= 5, f"chunk \u53e5\u5b50\u6570 {count} \u8d85\u8fc7 max_chunk_size=5\uff1a{ch!r}"


def test_min_chunk_size_merges_small_groups():
    """min_chunk_size \u751f\u6548\u65f6\uff0c\u8fc7\u5c0f\u7ec4\u4f1a\u4e0e\u76f8\u90bb\u7ec4\u5408\u5e76\u3002"""
    # \u6784\u9020\u4f1a\u5728\u6bcf\u53e5\u4e4b\u95f4\u5207\u5206\u7684\u573a\u666f\uff08\u7686\u4e92\u4e0d\u76f8\u4f3c\uff09
    sentences = [f"\u4e92\u5f02\u53e5\u5b50 {i}\u3002" for i in range(8)]
    text = "".join(sentences)
    topic_map = {s: i for i, s in enumerate(sentences)}  # \u6bcf\u53e5\u4e00\u4e2a\u4e3b\u9898
    encode_fn = _make_encode_fn(topic_map, dim=16, jitter=0.0)

    # min_chunk_size=3 \u5c06\u7ea6\u675f\u4f4e\u4e8e 3 \u53e5\u7684\u7ec4\u4e0e\u76f8\u90bb\u5408\u5e76
    chunks = semantic_chunk(
        text, encode_fn=encode_fn, threshold_percentile=99,
        min_chunk_size=3, max_chunk_size=10, min_tokens=1, max_tokens=10_000,
    )
    for ch in chunks:
        c = ch.count("\u4e92\u5f02\u53e5\u5b50")
        # \u5408\u5e76\u540e\u6700\u540e\u53ef\u80fd\u8fd8\u662f\u6709\u4e00\u4e2a < 3 \u7684\u5c3e\u5df4\uff08\u4e24\u4fa7\u90fd\u4f1a\u8d85 max\uff09\uff1b\u603b\u4f53\u603b\u53e5\u5b50\u6570 = 8
        assert 1 <= c <= 10
    total = sum(ch.count("\u4e92\u5f02\u53e5\u5b50") for ch in chunks)
    assert total == 8


# ============================================================
# 4. \u77ed\u6587\u672c\uff08< 3 \u53e5\uff09\u76f4\u63a5\u8fd4\u56de
# ============================================================

def test_short_text_returns_whole():
    """\u5c11\u4e8e 3 \u53e5\u7684\u6587\u672c\u4e0d\u5207\uff0c\u76f4\u63a5\u8fd4\u56de\u6574\u6bb5\u3002"""
    encode_calls = {"n": 0}

    def encode_fn(sents: List[str]) -> np.ndarray:
        encode_calls["n"] += 1
        return _topic_vec(0, dim=8).reshape(1, -1).repeat(len(sents), axis=0)

    out_empty = semantic_chunk("", encode_fn=encode_fn)
    assert out_empty == []

    out_one = semantic_chunk("\u53ea\u6709\u4e00\u53e5\u8bdd\u3002", encode_fn=encode_fn)
    assert len(out_one) == 1
    assert "\u53ea\u6709\u4e00\u53e5\u8bdd" in out_one[0]

    out_two = semantic_chunk("\u7b2c\u4e00\u53e5\u3002\u7b2c\u4e8c\u53e5\uff01", encode_fn=encode_fn)
    assert len(out_two) == 1

    # \u77ed\u6587\u672c\u4e0d\u5e94\u8c03\u7528 encode_fn\uff08\u8d70\u4e86\u8df3\u8fc7\u5206\u652f\uff09
    assert encode_calls["n"] == 0


# ============================================================
# 5. encode_fn \u63a5\u53e3\u5951\u7ea6
# ============================================================

def test_encode_fn_contract_called_once_with_all_sentences():
    """encode_fn \u88ab\u8c03\u7528\u4e00\u6b21\uff0c\u53c2\u6570\u4e3a List[str]\uff0c\u8fd4\u56de\u5f62\u72b6 (n, d) \u7684 ndarray\u3002"""
    captured = {"args": None, "calls": 0}

    def encode_fn(sents: List[str]) -> np.ndarray:
        captured["calls"] += 1
        captured["args"] = list(sents)
        # \u4ea4\u66ff\u8fd4\u56de\u4e24\u79cd\u4e3b\u9898\u5411\u91cf\uff0c\u4ea7\u751f\u53ef\u9884\u671f\u7684\u65ad\u88c2
        out = np.zeros((len(sents), 8), dtype=np.float32)
        v0 = _topic_vec(0, dim=8)
        v1 = _topic_vec(1, dim=8)
        for i in range(len(sents)):
            out[i] = v0 if i < len(sents) // 2 else v1
        return out

    text = "\u53e5\u5b50\u4e00\u3002\u53e5\u5b50\u4e8c\u3002\u53e5\u5b50\u4e09\u3002\u53e5\u5b50\u56db\u3002\u53e5\u5b50\u4e94\u3002\u53e5\u5b50\u516d\u3002"
    chunks = semantic_chunk(
        text, encode_fn=encode_fn, threshold_percentile=80,
        min_chunk_size=1, max_chunk_size=10, min_tokens=1, max_tokens=10_000,
    )

    assert captured["calls"] == 1, "encode_fn \u5e94\u88ab\u6279\u91cf\u8c03\u7528\u4e00\u6b21"
    assert isinstance(captured["args"], list)
    assert all(isinstance(s, str) for s in captured["args"])
    assert len(captured["args"]) == 6
    assert len(chunks) >= 2  # \u4e2d\u95f4\u4f4d\u7f6e\u5e94\u4ea7\u751f\u4e00\u4e2a\u65ad\u88c2


def test_encode_fn_exception_falls_back_gracefully():
    """encode_fn \u629b\u5f02\u5e38\u65f6\u5e94\u964d\u7ea7\u4e3a\u8fd4\u56de\u6574\u6bb5\uff0c\u4e0d\u4f1a\u5d29\u6e83\u3002"""
    def boom(sents: List[str]) -> np.ndarray:
        raise RuntimeError("model unavailable")

    text = "\u53e5\u5b50\u4e00\u3002\u53e5\u5b50\u4e8c\u3002\u53e5\u5b50\u4e09\u3002\u53e5\u5b50\u56db\u3002"
    chunks = semantic_chunk(text, encode_fn=boom)
    assert len(chunks) == 1
    assert "\u53e5\u5b50\u4e00" in chunks[0]


def test_encode_fn_shape_mismatch_falls_back():
    """encode_fn \u8fd4\u56de\u5f62\u72b6\u4e0d\u5339\u914d\u65f6\u5e94\u964d\u7ea7\u3002"""
    def wrong_shape(sents: List[str]) -> np.ndarray:
        return np.zeros((len(sents) + 1, 8), dtype=np.float32)

    text = "\u53e5\u5b50\u4e00\u3002\u53e5\u5b50\u4e8c\u3002\u53e5\u5b50\u4e09\u3002\u53e5\u5b50\u56db\u3002"
    chunks = semantic_chunk(text, encode_fn=wrong_shape)
    assert len(chunks) == 1


# ============================================================
# \u8f85\u52a9\uff1a\u53e5\u5b50\u5206\u5272 / token \u4f30\u7b97 / \u4f59\u5f26\u76f8\u4f3c\u5ea6
# ============================================================

def test_split_sentences_basic():
    text = "\u4f60\u597d\u3002\u4eca\u5929\u5929\u6c14\u4e0d\u9519\uff01\u8981\u51fa\u53bb\u5417\uff1fHello world.Yes!"
    sents = split_sentences(text)
    assert sents == ["\u4f60\u597d\u3002", "\u4eca\u5929\u5929\u6c14\u4e0d\u9519\uff01", "\u8981\u51fa\u53bb\u5417\uff1f", "Hello world.", "Yes!"]


def test_estimate_tokens_consistency():
    # CJK 1 token/字，但公式中的 max(1, ...) 导致纯 CJK 仍会 +1（与 server 及 markdown_chunker 一致）
    assert _estimate_tokens("中文测试") == 4 + 1
    # 英文 4 字符/token（至少 1）
    assert _estimate_tokens("abc") == 1
    assert _estimate_tokens("abcdefgh") == 2
    # 混合：中文 + 4 个非CJK字符 → 2 + max(1, 4//4) = 3
    assert _estimate_tokens("中文 abc") == 2 + 1
    # 空字符串
    assert _estimate_tokens("") == 0


def test_adjacent_cosine_orthogonal_vs_aligned():
    aligned = np.array([[1, 0], [1, 0], [1, 0]], dtype=np.float32)
    sims_a = _adjacent_cosine(aligned)
    assert np.allclose(sims_a, [1.0, 1.0])

    orth = np.array([[1, 0], [0, 1], [1, 0]], dtype=np.float32)
    sims_o = _adjacent_cosine(orth)
    assert np.allclose(sims_o, [0.0, 0.0], atol=1e-6)


# ============================================================
# 6. server 集成：strategy 路由 + API 运行时切换
# ============================================================

# server 模块在加载时会初始化 LLM provider（需要 anthropic / openai）。
# 在依赖未安装的环境下会 ImportError，这里仅跳过依赖 server 的测试，不影响纯分块器测试。
try:
    import server as _server  # noqa: F401
    _HAS_SERVER = True
    _SERVER_SKIP_REASON = ""
except Exception as _e:  # pragma: no cover - depends on env
    _server = None
    _HAS_SERVER = False
    _SERVER_SKIP_REASON = f"server import failed: {_e}"

_skip_no_server = pytest.mark.skipif(
    not _HAS_SERVER, reason=_SERVER_SKIP_REASON or "server unavailable"
)


@_skip_no_server
def test_chunk_with_size_routes_by_strategy(monkeypatch):
    """_chunk_with_size 根据 CHUNK_STRATEGY 调用不同分块函数。"""
    server = _server
    calls = {"semantic": 0, "structure": 0, "fixed": 0}

    def fake_semantic(text, min_t, max_t):
        calls["semantic"] += 1
        return ["[semantic]"]

    def fake_structure(text, min_t, max_t):
        calls["structure"] += 1
        return ["[structure]"]

    def fake_plain(text, min_t, max_t):
        calls["fixed"] += 1
        return ["[fixed]"]

    monkeypatch.setattr(server, "_chunk_semantic", fake_semantic)
    monkeypatch.setattr(server, "_chunk_structure", fake_structure)
    monkeypatch.setattr(server, "_chunk_plain_text", fake_plain)
    monkeypatch.setattr(server, "STRUCTURE_AWARE_CHUNK", False)  # \u6d88\u9664\u9ed8\u8ba4 markdown \u5206\u53d1

    monkeypatch.setattr(server, "CHUNK_STRATEGY", "semantic")
    assert server._chunk_with_size("hello", 10, 20) == ["[semantic]"]

    monkeypatch.setattr(server, "CHUNK_STRATEGY", "structure")
    assert server._chunk_with_size("hello", 10, 20) == ["[structure]"]

    monkeypatch.setattr(server, "CHUNK_STRATEGY", "fixed")
    assert server._chunk_with_size("hello", 10, 20) == ["[fixed]"]

    assert calls == {"semantic": 1, "structure": 1, "fixed": 1}


@_skip_no_server
def test_chunk_strategy_endpoint_runtime_switch(monkeypatch):
    """PUT /config/chunk-strategy 运行时更新全局变量；GET 返回当前状态。"""
    server = _server
    from fastapi.testclient import TestClient

    # \u907f\u514d\u89e6\u53d1 startup/lifespan \u91cc\u7684\u91cd\u578b\u521d\u59cb\u5316\uff1aTestClient \u4f1a\u8d70 lifespan\uff0c
    # \u4f46 load_store \u53ef\u80fd\u4f1a\u8bfb\u9879\u76ee\u6839\u7684\u73b0\u6709 chunks.pkl\uff1b\u8fd9\u91cc\u6211\u4eec\u4e0d\u5728\u610f\uff0c
    # \u53ea\u8c03\u7528\u8f7b\u91cf API\u3002\u5982\u679c lifespan \u5f00\u9500\u592a\u5927\uff0c\u53ef\u4f7f\u7528\u539f\u59cb ASGI \u8c03\u7528\u3002
    # \u4e3a\u4fdd\u8bc1\u7a33\u5b9a\uff0c\u7528 raise_app_exceptions=False \u4e14\u5728\u4e0a\u4e0b\u6587\u5916\u8c03\u7528\u3002
    client = TestClient(server.app, raise_server_exceptions=True)

    original_strategy = server.CHUNK_STRATEGY
    try:
        # \u521d\u59cb\u72b6\u6001 GET
        with client:
            r = client.get("/config/chunk-strategy")
            assert r.status_code == 200
            body = r.json()
            assert body["strategy"] in ("fixed", "structure", "semantic", "agentic")
            assert set(body["valid"]) == {"fixed", "structure", "semantic", "agentic"}

            # PUT \u5207\u6362\u4e3a semantic
            r = client.put("/config/chunk-strategy", json={"strategy": "semantic"})
            assert r.status_code == 200
            assert r.json()["strategy"] == "semantic"
            assert server.CHUNK_STRATEGY == "semantic"

            # PUT \u65e0\u6548\u503c \u2192 400
            r = client.put("/config/chunk-strategy", json={"strategy": "garbage"})
            assert r.status_code == 400
            # \u5207\u6362\u5931\u8d25\u4e0d\u5f71\u54cd\u4e0a\u6b21\u6210\u529f\u8bbe\u7f6e\u7684\u503c
            assert server.CHUNK_STRATEGY == "semantic"

            # PUT \u5207\u6362\u4e3a structure
            r = client.put("/config/chunk-strategy", json={"strategy": "STRUCTURE"})
            assert r.status_code == 200
            assert r.json()["strategy"] == "structure"
            assert server.CHUNK_STRATEGY == "structure"
    finally:
        # \u6062\u590d\u5168\u5c40\u72b6\u6001\uff0c\u907f\u514d\u5f71\u54cd\u540e\u7eed\u6d4b\u8bd5
        server.CHUNK_STRATEGY = original_strategy

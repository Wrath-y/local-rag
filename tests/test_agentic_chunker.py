"""Agentic Chunking 单元测试。

覆盖点：
1. 正常 LLM 返回时的分块结果
2. LLM 返回格式不规范时的降级处理
3. LLM 调用失败时的降级到 structure 策略
4. generate_summary 开关控制
5. max_llm_input_tokens 截断逻辑（超长文档分段）
6. 超长文档分段处理
7. strategy 路由正确（server.CHUNK_STRATEGY == "agentic" 时走 agentic 路径）
8. API 端点支持 "agentic" 值
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
from typing import List

import pytest

# 保证项目根目录在 sys.path 中
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

from agentic_chunker import (  # noqa: E402
    agentic_chunk,
    _estimate_tokens,
    _extract_json,
    _normalize_boundaries,
    _segment_text,
    _split_oversize_text,
    _build_prompt,
)


# ============================================================
# 辅助：mock LLM 函数工厂
# ============================================================

def _make_llm_fn(response: str | Exception):
    """创建返回固定响应或抛异常的 async LLM mock。"""
    async def _fn(prompt: str) -> str:
        if isinstance(response, Exception):
            raise response
        return response
    return _fn


def _run(coro):
    """同步运行异步协程（测试用）。"""
    return asyncio.run(coro)


# ============================================================
# 1. 正常 LLM 返回时的分块结果
# ============================================================

class TestNormalChunking:
    def test_basic_two_chunks(self):
        """LLM 正常返回 2 个 chunk 边界，分块正确。"""
        doc_lines = [
            "# 介绍",
            "这是一个关于Redis的文档。",
            "Redis是一个内存数据库。",
            "",
            "# 配置",
            "port=6379",
            "timeout=300",
            "maxmemory=256mb",
        ]
        text = "\n".join(doc_lines)

        llm_response = json.dumps({
            "chunks": [
                {"start_line": 1, "end_line": 3, "summary": "Redis简介"},
                {"start_line": 4, "end_line": 8, "summary": "Redis配置参数"},
            ]
        })
        llm_fn = _make_llm_fn(llm_response)
        logs: List[str] = []

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            generate_summary=True,
            max_llm_input_tokens=4000,
            min_tokens=1, max_tokens=10000,
            log_fn=logs.append,
        ))

        assert len(result) == 2
        assert "Redis简介" in result[0]
        assert "# 介绍" in result[0]
        assert "Redis配置参数" in result[1]
        assert "port=6379" in result[1]

    def test_single_chunk_whole_document(self):
        """LLM 认为整篇是一个 chunk。"""
        text = "这是一段短文。\n没有必要拆分。\n整体即可。"
        llm_response = json.dumps({
            "chunks": [
                {"start_line": 1, "end_line": 3, "summary": "一段短文"},
            ]
        })
        llm_fn = _make_llm_fn(llm_response)

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            generate_summary=True,
            min_tokens=1, max_tokens=10000,
            log_fn=lambda x: None,
        ))

        assert len(result) == 1
        assert "一段短文" in result[0]

    def test_empty_text_returns_empty(self):
        """空文本直接返回空列表。"""
        llm_fn = _make_llm_fn("should not be called")
        result = _run(agentic_chunk("", llm_fn=llm_fn, log_fn=lambda x: None))
        assert result == []

        result2 = _run(agentic_chunk("   ", llm_fn=llm_fn, log_fn=lambda x: None))
        assert result2 == []


# ============================================================
# 2. LLM 返回格式不规范时的降级处理
# ============================================================

class TestInvalidJsonFallback:
    def test_non_json_response(self):
        """LLM 返回非 JSON 文本 → 降级。"""
        text = "第一行内容。\n第二行内容。\n第三行内容。"
        llm_fn = _make_llm_fn("这不是JSON，只是普通文本")
        fallback_called = {"n": 0}

        def fallback(t, min_t, max_t):
            fallback_called["n"] += 1
            return ["[fallback chunk]"]

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            fallback_fn=fallback,
            log_fn=lambda x: None,
        ))

        assert fallback_called["n"] == 1
        assert result == ["[fallback chunk]"]

    def test_json_missing_chunks_key(self):
        """LLM 返回合法 JSON 但缺少 chunks 字段 → 降级。"""
        text = "内容A。\n内容B。\n内容C。"
        llm_fn = _make_llm_fn(json.dumps({"segments": [1, 2, 3]}))
        fallback_called = {"n": 0}

        def fallback(t, min_t, max_t):
            fallback_called["n"] += 1
            return ["[fallback]"]

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            fallback_fn=fallback,
            log_fn=lambda x: None,
        ))

        assert fallback_called["n"] == 1

    def test_json_with_markdown_fence(self):
        """LLM 返回带 ```json 围栏的 JSON → 应正确解析。"""
        text = "行1\n行2\n行3"
        fenced = '```json\n{"chunks":[{"start_line":1,"end_line":3,"summary":"全部"}]}\n```'
        llm_fn = _make_llm_fn(fenced)

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            generate_summary=True,
            min_tokens=1, max_tokens=10000,
            log_fn=lambda x: None,
        ))

        assert len(result) == 1
        assert "全部" in result[0]

    def test_invalid_boundaries_fallback(self):
        """LLM 返回的边界全部无效（非数字）→ 降级。"""
        text = "行1\n行2\n行3"
        bad_json = json.dumps({
            "chunks": [
                {"start_line": "abc", "end_line": "xyz"},
            ]
        })
        llm_fn = _make_llm_fn(bad_json)
        fallback_called = {"n": 0}

        def fallback(t, min_t, max_t):
            fallback_called["n"] += 1
            return ["[fallback]"]

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            fallback_fn=fallback,
            log_fn=lambda x: None,
        ))

        assert fallback_called["n"] == 1


# ============================================================
# 3. LLM 调用失败时的降级到 structure 策略
# ============================================================

class TestLLMFailureFallback:
    def test_llm_exception_triggers_fallback(self):
        """LLM 抛异常 → 调用 fallback_fn。"""
        text = "一些文本。\n还有一些。\n第三行。"
        llm_fn = _make_llm_fn(RuntimeError("API timeout"))
        fallback_called = {"n": 0}

        def fallback(t, min_t, max_t):
            fallback_called["n"] += 1
            return ["[structure fallback]"]

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            fallback_fn=fallback,
            log_fn=lambda x: None,
        ))

        assert fallback_called["n"] == 1
        assert result == ["[structure fallback]"]

    def test_no_fallback_fn_returns_whole_text(self):
        """没有提供 fallback_fn 时，返回整段原文作为兜底。"""
        text = "完整内容。\n不会丢失。"
        llm_fn = _make_llm_fn(RuntimeError("network error"))

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            fallback_fn=None,
            log_fn=lambda x: None,
        ))

        assert len(result) == 1
        assert "完整内容" in result[0]
        assert "不会丢失" in result[0]


# ============================================================
# 4. generate_summary 开关控制
# ============================================================

class TestSummarySwitch:
    def test_summary_enabled(self):
        """generate_summary=True 时 chunk 前缀含 [摘要]。"""
        text = "行1\n行2\n行3"
        llm_response = json.dumps({
            "chunks": [{"start_line": 1, "end_line": 3, "summary": "测试摘要"}]
        })
        llm_fn = _make_llm_fn(llm_response)

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            generate_summary=True,
            min_tokens=1, max_tokens=10000,
            log_fn=lambda x: None,
        ))

        assert len(result) == 1
        assert "[摘要] 测试摘要" in result[0]

    def test_summary_disabled(self):
        """generate_summary=False 时 chunk 不含 [摘要] 前缀。"""
        text = "行1\n行2\n行3"
        llm_response = json.dumps({
            "chunks": [{"start_line": 1, "end_line": 3, "summary": "应忽略"}]
        })
        llm_fn = _make_llm_fn(llm_response)

        result = _run(agentic_chunk(
            text, llm_fn=llm_fn,
            generate_summary=False,
            min_tokens=1, max_tokens=10000,
            log_fn=lambda x: None,
        ))

        assert len(result) == 1
        assert "[摘要]" not in result[0]
        assert "行1" in result[0]


# ============================================================
# 5. max_llm_input_tokens 截断逻辑
# ============================================================

class TestSegmentation:
    def test_short_doc_single_segment(self):
        """短文档不分段，直接单次发给 LLM。"""
        segments = _segment_text("short", 4000)
        assert len(segments) == 1

    def test_long_doc_multi_segments(self):
        """超长文档被分为多段。"""
        # 生成大量行，每行约 10 tokens
        lines = [f"这是第{i}行的内容信息数据" for i in range(200)]
        text = "\n".join(lines)
        segments = _segment_text(text, 500)
        assert len(segments) > 1
        # 所有行都应被包含（无数据丢失）
        total_lines = sum(s.count("\n") + 1 for s in segments)
        assert total_lines >= 200

    def test_multi_segment_agentic_chunk(self):
        """超长文档分段后，每段独立调用 LLM，结果合并。"""
        # 构造两段，max_llm_input_tokens 设为很小值强制分段
        lines = [f"段落内容第{i}行。" for i in range(20)]
        text = "\n".join(lines)

        call_count = {"n": 0}

        async def counting_llm(prompt: str) -> str:
            call_count["n"] += 1
            # 根据段中有多少行生成对应的 JSON
            # 简单策略：所有行作为一个 chunk
            import re
            # 找 prompt 中最后出现的行号来确定段行数
            nums = re.findall(r"^(\d+):", prompt, re.MULTILINE)
            if not nums:
                n = 1
            else:
                n = len(nums)
            return json.dumps({
                "chunks": [{"start_line": 1, "end_line": n, "summary": f"chunk{call_count['n']}"}]
            })

        result = _run(agentic_chunk(
            text, llm_fn=counting_llm,
            generate_summary=True,
            max_llm_input_tokens=50,  # 很小，强制分段
            min_tokens=1, max_tokens=10000,
            log_fn=lambda x: None,
        ))

        # 应该被分了多段
        assert call_count["n"] > 1
        # 结果应包含所有内容
        joined = "\n".join(result)
        assert "段落内容第0行" in joined
        assert "段落内容第19行" in joined


# ============================================================
# 6. 超长 chunk 后处理（split_oversize_text）
# ============================================================

class TestOversizeHandling:
    def test_split_oversize_chunk(self):
        """单个 chunk 超过 max_tokens 时被二次切分。"""
        # 多行文本，每行约 10 tokens，总计约 500 tokens
        lines = [f"内容行{i}的详细信息数据" for i in range(50)]
        long_text = "\n".join(lines)
        pieces = _split_oversize_text(long_text, max_tokens=100)
        assert len(pieces) > 1
        # 拼回来不丢内容
        joined = "\n".join(pieces)
        for i in range(50):
            assert f"内容行{i}的详细信息数据" in joined

    def test_within_limit_not_split(self):
        """不超限的文本不被切分。"""
        text = "short"
        pieces = _split_oversize_text(text, max_tokens=1000)
        assert pieces == [text]


# ============================================================
# 7. strategy 路由正确（server 集成）
# ============================================================

# server 模块在加载时会初始化模型等重型资源，某些环境可能不可用
try:
    import server as _server  # noqa: F401
    _HAS_SERVER = True
    _SERVER_SKIP_REASON = ""
except Exception as _e:
    _server = None
    _HAS_SERVER = False
    _SERVER_SKIP_REASON = f"server import failed: {_e}"

_skip_no_server = pytest.mark.skipif(
    not _HAS_SERVER, reason=_SERVER_SKIP_REASON or "server unavailable"
)


@_skip_no_server
def test_chunk_with_size_routes_agentic(monkeypatch):
    """CHUNK_STRATEGY=agentic 时 _chunk_with_size 调用 _chunk_agentic。"""
    server = _server
    calls = {"agentic": 0}

    def fake_agentic(text, min_t, max_t):
        calls["agentic"] += 1
        return ["[agentic result]"]

    monkeypatch.setattr(server, "_chunk_agentic", fake_agentic)
    monkeypatch.setattr(server, "CHUNK_STRATEGY", "agentic")

    result = server._chunk_with_size("some text", 200, 400)
    assert result == ["[agentic result]"]
    assert calls["agentic"] == 1


@_skip_no_server
def test_agentic_strategy_in_valid_set():
    """_VALID_CHUNK_STRATEGIES 包含 'agentic'。"""
    server = _server
    assert "agentic" in server._VALID_CHUNK_STRATEGIES


# ============================================================
# 8. API 端点支持 "agentic" 值
# ============================================================

@_skip_no_server
def test_chunk_strategy_endpoint_accepts_agentic(monkeypatch):
    """PUT /config/chunk-strategy 接受 'agentic' 值。"""
    server = _server
    from fastapi.testclient import TestClient

    original_strategy = server.CHUNK_STRATEGY
    try:
        with TestClient(server.app, raise_server_exceptions=True) as client:
            # PUT 切换为 agentic
            r = client.put("/config/chunk-strategy", json={"strategy": "agentic"})
            assert r.status_code == 200
            assert r.json()["strategy"] == "agentic"
            assert server.CHUNK_STRATEGY == "agentic"

            # GET 返回 agentic 参数
            r = client.get("/config/chunk-strategy")
            assert r.status_code == 200
            body = r.json()
            assert "agentic" in body["valid"]
            assert "agentic" in body
            assert "generate_summary" in body["agentic"]
            assert "max_llm_input_tokens" in body["agentic"]
    finally:
        server.CHUNK_STRATEGY = original_strategy


# ============================================================
# 辅助函数单元测试
# ============================================================

class TestHelpers:
    def test_estimate_tokens(self):
        assert _estimate_tokens("") == 0
        assert _estimate_tokens("中文") == 2 + 1  # 2 CJK + max(1, 0//4)
        assert _estimate_tokens("abcdefgh") == 2

    def test_extract_json_plain(self):
        obj = _extract_json('{"chunks": [{"start_line": 1, "end_line": 5}]}')
        assert obj is not None
        assert "chunks" in obj

    def test_extract_json_with_fence(self):
        raw = '一些废话\n```json\n{"chunks":[]}\n```\n更多废话'
        obj = _extract_json(raw)
        assert obj == {"chunks": []}

    def test_extract_json_garbage(self):
        assert _extract_json("not json at all") is None
        assert _extract_json("") is None
        assert _extract_json(None) is None  # type: ignore[arg-type]

    def test_normalize_boundaries_basic(self):
        raw = [
            {"start_line": 1, "end_line": 5, "summary": "A"},
            {"start_line": 6, "end_line": 10, "summary": "B"},
        ]
        result = _normalize_boundaries(raw, 10)
        assert len(result) == 2
        assert result[0]["start_line"] == 1
        assert result[1]["end_line"] == 10

    def test_normalize_boundaries_overlap_fix(self):
        """重叠边界被修正。"""
        raw = [
            {"start_line": 1, "end_line": 7},
            {"start_line": 5, "end_line": 10},
        ]
        result = _normalize_boundaries(raw, 10)
        assert len(result) == 2
        # 第二段的 start 应被抬到第一段 end + 1
        assert result[1]["start_line"] == result[0]["end_line"] + 1

    def test_normalize_boundaries_covers_full_doc(self):
        """首段从1开始，末段到 n_lines 结束。"""
        raw = [
            {"start_line": 3, "end_line": 5},
            {"start_line": 7, "end_line": 8},
        ]
        result = _normalize_boundaries(raw, 10)
        assert result[0]["start_line"] == 1
        assert result[-1]["end_line"] == 10

    def test_build_prompt_contains_numbered_lines(self):
        text = "第一行\n第二行\n第三行"
        prompt = _build_prompt(text, 200, 400, True)
        assert "1: 第一行" in prompt
        assert "2: 第二行" in prompt
        assert "3: 第三行" in prompt
        assert "200-400" in prompt

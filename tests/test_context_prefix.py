"""上下文强化（标题面包屑前缀）单元测试。

覆盖场景：
    1. context_prefix 关闭时 chunks 无前缀（向后兼容）。
    2. 开启时 h1/h2/h3 正确传播。
    3. 同级标题替换（h2 遇到新 h2，旧的被清除）。
    4. max_depth 限制生效（超过深度的标题不传播）。
    5. breadcrumb 格式正确（`[A > B > C]\\n`）。
    6. 非 Markdown 文本不受影响（looks_like_markdown 返回 False）。
    7. 与层次化分块配合：parent chunk 也包含其标题前缀。
"""

from __future__ import annotations

import os
import sys

# 保证项目根目录在 sys.path 中，便于直接 `pytest` 调用
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _ROOT not in sys.path:
    sys.path.insert(0, _ROOT)

import pytest

from markdown_chunker import (
    Block,
    build_heading_prefix,
    chunk_markdown,
    looks_like_markdown,
)


# ========== 1. 关闭时无前缀（向后兼容） ==========

def test_context_prefix_disabled_no_prefix_added():
    md = "# 顶层标题\n\n正文段落，简短内容。"
    chunks = chunk_markdown(md, min_tokens=200, max_tokens=400)
    assert len(chunks) >= 1
    # 关闭场景下任何 chunk 不应以面包屑 [ 开头
    for c in chunks:
        assert not c.startswith("[")


def test_context_prefix_disabled_equals_legacy_output():
    """关闭场景下输出应与不显式传该参数时完全一致。"""
    md = (
        "# 章节A\n\n"
        + ("内容" * 80) + "\n\n"
        + "## 子节A1\n\n"
        + ("文字" * 80) + "\n"
    )
    legacy = chunk_markdown(md, min_tokens=100, max_tokens=200)
    explicit_off = chunk_markdown(
        md, min_tokens=100, max_tokens=200,
        context_prefix_enabled=False,
    )
    assert legacy == explicit_off


# ========== 2. h1/h2/h3 正确传播 ==========

def test_breadcrumb_propagates_h1_h2_h3():
    md = (
        "# 性能优化\n\n"
        "## Redis配置\n\n"
        "### 缓存策略\n\n"
        + ("内容" * 120)  # 触发 flush，使内容段落进入新 chunk 携带面包屑
    )
    chunks = chunk_markdown(
        md, min_tokens=100, max_tokens=200,
        context_prefix_enabled=True,
        context_prefix_max_depth=3,
    )
    # 至少存在一个含有正文的 chunk，其面包屑应包含三级标题路径
    joined = "\n---\n".join(chunks)
    assert "[性能优化 > Redis配置 > 缓存策略]" in joined


# ========== 3. 同级标题替换 ==========

def test_sibling_heading_replaces_previous():
    md = (
        "# 顶层\n\n"
        "## 旧子节\n\n"
        + ("旧内容" * 80) + "\n\n"
        "## 新子节\n\n"
        + ("新内容" * 80) + "\n"
    )
    chunks = chunk_markdown(
        md, min_tokens=80, max_tokens=160,
        context_prefix_enabled=True,
        context_prefix_max_depth=3,
    )
    # 含"新内容"的 chunk 的面包屑应反映"新子节"，且不能仍标注"旧子节"
    new_chunks = [c for c in chunks if "新内容" in c]
    assert new_chunks, "应存在包含'新内容'的 chunk"
    for c in new_chunks:
        if c.startswith("["):
            head_line = c.split("\n", 1)[0]
            assert "新子节" in head_line or "顶层" in head_line
            assert "旧子节" not in head_line


# ========== 4. max_depth 限制 ==========

def test_max_depth_limits_breadcrumb_levels():
    stack = [
        Block(type="heading", text="# L1", level=1),
        Block(type="heading", text="## L2", level=2),
        Block(type="heading", text="### L3", level=3),
        Block(type="heading", text="#### L4", level=4),
    ]
    prefix = build_heading_prefix(stack, max_depth=2, format="breadcrumb")
    # 应只保留栈尾两个：L3 > L4
    assert prefix == "[L3 > L4]\n"

    # max_depth=3 → L2 > L3 > L4
    prefix3 = build_heading_prefix(stack, max_depth=3)
    assert prefix3 == "[L2 > L3 > L4]\n"


def test_max_depth_zero_returns_empty():
    stack = [Block(type="heading", text="# A", level=1)]
    assert build_heading_prefix(stack, max_depth=0) == ""


# ========== 5. breadcrumb 格式正确 ==========

def test_breadcrumb_format_strips_hash_marks():
    stack = [
        Block(type="heading", text="# 性能优化", level=1),
        Block(type="heading", text="## Redis配置", level=2),
        Block(type="heading", text="### 缓存策略", level=3),
    ]
    prefix = build_heading_prefix(stack, max_depth=3, format="breadcrumb")
    # 标题中的 # 标记应被剥离，仅保留文字
    assert prefix == "[性能优化 > Redis配置 > 缓存策略]\n"
    assert "#" not in prefix


def test_empty_stack_returns_empty_prefix():
    assert build_heading_prefix([], max_depth=3) == ""


# ========== 6. 非 Markdown 文本不受影响 ==========

def test_plain_text_not_routed_through_markdown_path():
    plain = "今天天气不错。我们去公园散步吧。然后顺便买点水果回家。"
    assert looks_like_markdown(plain) is False
    # 直接调用 chunk_markdown 也不会产生面包屑（无标题）
    chunks = chunk_markdown(
        plain, min_tokens=10, max_tokens=100,
        context_prefix_enabled=True,
    )
    for c in chunks:
        assert not c.startswith("[")


def test_markdown_without_headings_no_prefix():
    md = "```python\nprint('hi')\n```\n\n后续正文，不含任何标题。"
    chunks = chunk_markdown(
        md, min_tokens=10, max_tokens=200,
        context_prefix_enabled=True,
    )
    assert chunks
    for c in chunks:
        assert not c.startswith("[")


# ========== 7. 与层次化分块配合 ==========

def test_hierarchical_parents_carry_breadcrumb(monkeypatch):
    """层次化模式下，parent chunks 也应带有面包屑前缀。

    通过直接调用 _chunk_with_size 验证 server.py 中调用链能正确透传。
    """
    try:
        import server  # noqa: F401
    except Exception:
        pytest.skip("server 模块导入失败，跳过该集成检查")

    # 强制开启上下文前缀（不修改 config.yaml）
    monkeypatch.setattr(server, "CONTEXT_PREFIX_ENABLED", True)
    monkeypatch.setattr(server, "CONTEXT_PREFIX_MAX_DEPTH", 3)
    monkeypatch.setattr(server, "CONTEXT_PREFIX_FORMAT", "breadcrumb")
    monkeypatch.setattr(server, "STRUCTURE_AWARE_CHUNK", True)

    md = (
        "# 性能优化\n\n"
        "## Redis配置\n\n"
        + ("缓存内容" * 100) + "\n\n"
        + ("更多内容" * 100) + "\n"
    )
    # parent 尺寸
    parents = server._chunk_with_size(md, 200, 400)
    assert parents
    # 至少一个 parent 携带面包屑前缀
    assert any(p.startswith("[") for p in parents)

    # 调用层次化接口：parent 与 child 都应是 Markdown 路径产物
    results = server.chunk_text_hierarchical(
        md, min_tokens=100, max_tokens=200, parent_max_tokens=400,
    )
    assert results
    # parent_text 字段（如存在）也应包含面包屑前缀
    parent_texts = {r["parent_text"] for r in results if r.get("parent_text")}
    if parent_texts:
        assert any(pt.startswith("[") for pt in parent_texts)


def test_hierarchical_disabled_context_prefix_no_change(monkeypatch):
    """关闭上下文前缀时，层次化分块结果不应包含面包屑标记。"""
    try:
        import server  # noqa: F401
    except Exception:
        pytest.skip("server 模块导入失败，跳过该集成检查")

    monkeypatch.setattr(server, "CONTEXT_PREFIX_ENABLED", False)
    monkeypatch.setattr(server, "STRUCTURE_AWARE_CHUNK", True)

    md = (
        "# 顶层\n\n"
        "## 二级\n\n"
        + ("段落" * 100) + "\n"
    )
    parents = server._chunk_with_size(md, 200, 400)
    assert parents
    for p in parents:
        assert not p.startswith("[")

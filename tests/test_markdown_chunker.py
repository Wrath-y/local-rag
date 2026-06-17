"""结构感知 Markdown 分块器的单元测试。

仅依赖 markdown_chunker 模块本身，不引入 server 重型导入，运行迅速。
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
    chunk_markdown,
    estimate_tokens,
    looks_like_markdown,
    parse_blocks,
)


# ========== 辅助 ==========

def _count_fences(text: str) -> int:
    """统计字符串中 ``` 围栏的出现次数。"""
    return text.count("```")


# ========== 边界情况 ==========

def test_empty_text_returns_empty_list():
    assert chunk_markdown("") == []
    assert chunk_markdown("   \n\n  \t  ") == []


def test_parse_blocks_on_empty_returns_empty():
    assert parse_blocks("") == []
    assert parse_blocks("   \n\n  ") == []


def test_estimate_tokens_basic():
    # 与 server.chunk_text 一致：非 CJK 部分至少计 1 token，所以 4 个 CJK 字会返回 5
    assert estimate_tokens("你好世界") == 5
    # 纯英文：4 字符/token，且至少 1 token
    assert estimate_tokens("abcd") == 1
    assert estimate_tokens("abcdefgh") == 2
    # 混合
    assert estimate_tokens("你好abcd") >= 2


# ========== Markdown 检测 ==========

def test_looks_like_markdown_detects_signals():
    assert looks_like_markdown("# Title\nbody") is True
    assert looks_like_markdown("text\n```python\ncode\n```") is True
    assert looks_like_markdown("intro\n| a | b |\n| - | - |\n| 1 | 2 |") is True


def test_looks_like_markdown_plain_text_false():
    plain = "这是一段纯文本，没有任何 Markdown 标志。仅普通句号与逗号。"
    assert looks_like_markdown(plain) is False
    assert looks_like_markdown("") is False


# ========== 表格不被分割 ==========

def test_table_is_atomic_even_when_oversized():
    # 构造一个包含大量行的表格，使其 token 数远超 max_tokens
    header = "| col1 | col2 | col3 |"
    sep = "| --- | --- | --- |"
    rows = "\n".join(
        f"| 单元格数据{i}aaaa | 单元格数据{i}bbbb | 单元格数据{i}cccc |"
        for i in range(60)
    )
    md = f"# 标题\n\n{header}\n{sep}\n{rows}\n"

    chunks = chunk_markdown(md, min_tokens=50, max_tokens=120)

    # 表格应作为单一原子块出现在某个 chunk 内：分隔行只应出现一次
    occurrences = sum(c.count(sep) for c in chunks)
    assert occurrences == 1, f"表格被切分了，分隔行出现 {occurrences} 次"

    # 而且数据行全部保留在同一个 chunk 中
    target = next(c for c in chunks if sep in c)
    for i in range(60):
        assert f"单元格数据{i}aaaa" in target


# ========== 代码块保持 ``` 闭合 ==========

def test_code_block_keeps_fence_paired():
    code_lines = "\n".join(f"print('line {i}')" for i in range(80))
    md = f"# 文档\n\n介绍段落。\n\n```python\n{code_lines}\n```\n\n后续段落。\n"

    chunks = chunk_markdown(md, min_tokens=50, max_tokens=100)

    # 找到包含代码的 chunk，验证 ``` 成对出现
    code_chunks = [c for c in chunks if "```" in c]
    assert code_chunks, "代码块应至少存在于一个 chunk 中"
    for c in code_chunks:
        assert _count_fences(c) % 2 == 0, f"代码块 fence 未成对：{c!r}"


def test_unclosed_code_block_is_force_closed():
    md = "正文\n\n```python\nprint('hi')\n# 缺失结尾 fence"
    chunks = chunk_markdown(md, min_tokens=10, max_tokens=200)
    joined = "\n".join(chunks)
    # 兜底逻辑应补齐围栏，使 ``` 成对
    assert _count_fences(joined) % 2 == 0


# ========== 标题与后续内容合并 ==========

def test_heading_merges_with_following_content():
    md = "# 我的标题\n\n这是一段简短的正文，不超过 min_tokens。"
    chunks = chunk_markdown(md, min_tokens=200, max_tokens=400)
    assert len(chunks) == 1
    assert "我的标题" in chunks[0]
    assert "这是一段简短的正文" in chunks[0]


def test_heading_only_input_still_emits_chunk():
    md = "# 仅有一个标题"
    chunks = chunk_markdown(md, min_tokens=200, max_tokens=400)
    assert len(chunks) == 1
    assert "仅有一个标题" in chunks[0]


def test_heading_prefix_carried_to_next_chunk():
    # 一个长段落足以在标题之后立即触发 flush，下一段时应携带标题前缀
    long_para_a = "字" * 250  # 约 250 tokens（CJK 1 token/字）
    long_para_b = "符" * 250
    md = f"# 顶层标题\n\n{long_para_a}\n\n{long_para_b}\n"

    chunks = chunk_markdown(md, min_tokens=200, max_tokens=400)
    assert len(chunks) >= 2
    # 第一个 chunk 应包含标题
    assert "顶层标题" in chunks[0]
    # 后续 chunk 起始应注入标题作为上下文前缀
    assert any("顶层标题" in c for c in chunks[1:])


# ========== Token 预算控制 ==========

def test_normal_paragraphs_respect_max_tokens():
    # 一组中等大小的段落，单块均不超 max；总和需多次 flush
    paragraphs = [f"段落{i}：" + ("内容" * 30) for i in range(8)]
    md = "\n\n".join(paragraphs)
    chunks = chunk_markdown(md, min_tokens=100, max_tokens=200)

    assert len(chunks) >= 2
    for c in chunks:
        # 每个由普通段落组合的 chunk 应不超过 max_tokens（允许少量边界波动 +20%）
        assert estimate_tokens(c) <= 200 * 1.2


def test_oversized_atomic_block_emitted_intact():
    # 构造一个超出 max_tokens 的代码块（原子块），应独立成 chunk 且保持完整
    code = "\n".join(f"line_{i} = {i}" for i in range(200))
    md = f"前言段落。\n\n```python\n{code}\n```\n\n结尾段落。"
    chunks = chunk_markdown(md, min_tokens=50, max_tokens=80)

    # 包含 ``` 的 chunk 内 fence 成对，且包含整个代码块
    code_chunks = [c for c in chunks if "```" in c]
    assert len(code_chunks) == 1
    target = code_chunks[0]
    assert _count_fences(target) == 2
    assert "line_0 = 0" in target
    assert "line_199 = 199" in target


# ========== 列表作为整体 ==========

def test_list_kept_as_single_block_when_fits():
    md = (
        "# 待办\n\n"
        "- 第一项需要完成的任务，描述较短\n"
        "- 第二项需要完成的任务，描述较短\n"
        "- 第三项需要完成的任务，描述较短\n"
    )
    blocks = parse_blocks(md)
    list_blocks = [b for b in blocks if b.type == "list"]
    assert len(list_blocks) == 1
    assert "第一项" in list_blocks[0].text
    assert "第三项" in list_blocks[0].text


# ========== 解析正确性 ==========

def test_parse_blocks_classifies_types():
    md = (
        "# H1\n"
        "\n"
        "段落正文。\n"
        "\n"
        "```python\n"
        "x = 1\n"
        "```\n"
        "\n"
        "| a | b |\n"
        "| - | - |\n"
        "| 1 | 2 |\n"
        "\n"
        "- item one\n"
        "- item two\n"
    )
    blocks = parse_blocks(md)
    types = [b.type for b in blocks]
    assert "heading" in types
    assert "para" in types
    assert "code" in types
    assert "table" in types
    assert "list" in types


# ========== 非 Markdown 文本路由 ==========

def test_plain_text_does_not_trigger_markdown_path():
    """非 Markdown 文本的检测函数应返回 False，让上游走原句子级逻辑。"""
    plain_zh = "今天天气不错。我们去公园散步吧。然后顺便买点水果回家。"
    plain_en = "Just some plain prose without any markers, separated by punctuation."
    assert looks_like_markdown(plain_zh) is False
    assert looks_like_markdown(plain_en) is False


# ========== 与 server.chunk_text 集成（轻量校验） ==========

@pytest.mark.skipif(
    os.environ.get("SKIP_SERVER_IMPORT") == "1",
    reason="跳过 server 集成检查（设置 SKIP_SERVER_IMPORT=1 时）",
)
def test_server_chunk_text_routes_markdown_through_new_path():
    """集成校验：server.chunk_text 对 Markdown 输入会走结构感知路径。

    通过观察 ``` 是否在产出的 chunk 中保持成对来间接验证。
    若 server 模块导入开销过大，可设置 SKIP_SERVER_IMPORT=1 跳过。
    """
    try:
        import server  # noqa: F401
    except Exception:
        pytest.skip("server 模块导入失败，跳过该集成检查")

    md = (
        "# 标题\n\n"
        "前言。\n\n"
        "```python\n"
        + "\n".join(f"v_{i} = {i}" for i in range(50))
        + "\n```\n\n"
        "结尾。\n"
    )
    chunks = server.chunk_text(md)
    joined = "\n".join(chunks)
    assert _count_fences(joined) % 2 == 0

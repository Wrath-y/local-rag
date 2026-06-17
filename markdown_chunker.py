"""结构感知的 Markdown 分块器。

将 Markdown 文本解析为原子块（标题 / 代码块 / 表格 / 列表 / 普通段落），
然后按 token 预算智能打包，避免在表格、代码块、列表中间被截断。

设计要点：
- token 估算与 server.chunk_text() 保持一致：CJK 1 token/字，其他 4 字符/token。
- 标题块不单独成 chunk，始终与后续内容合并。
- 单个原子块（如大表格 / 长代码块）超过 max_tokens 时保持完整，不再拆分。
- 在 chunk 边界处，将前一 chunk 的最后一个标题行作为下一 chunk 的前缀，提供上下文连续性。
- 仅依赖标准库（re、dataclasses、typing），不引入额外外部依赖。
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import List, Optional, Tuple


# ============================================================
# Token 估算（与 server.chunk_text 中的算法保持一致）
# ============================================================

def estimate_tokens(text: str) -> int:
    """估算文本的 token 数：CJK 1 token/字，其他 4 字符/token。"""
    if not text:
        return 0
    cjk = sum(1 for c in text if '\u4e00' <= c <= '\u9fff')
    # 与 server.py 的 chunk_text 保持一致：非 CJK 部分至少计 1 token
    return cjk + max(1, (len(text) - cjk) // 4)


# ============================================================
# 原子块定义
# ============================================================

# 块类型：'heading' | 'code' | 'table' | 'list' | 'para'
BlockType = str


@dataclass
class Block:
    type: BlockType
    text: str
    level: int = 0  # 仅 heading 类型有效（1~6）


# ============================================================
# Markdown 解析
# ============================================================

# 标题：# / ## / ... / ######
_HEADING_RE = re.compile(r'^(#{1,6})\s+(.*)$')

# 围栏代码块起止：``` 或 ~~~（可附语言标识）
_FENCE_RE = re.compile(r'^(```|~~~)')

# 表格分隔行：例如 | --- | :---: | ---: |，也允许单 -
_TABLE_SEP_RE = re.compile(
    r'^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)+\|?\s*$'
)

# 列表项起始：- / * / + / 1.
_LIST_RE = re.compile(r'^(\s*)([-*+]|\d+\.)\s+')


def _is_table_start(lines: List[str], i: int) -> bool:
    """判定第 i 行是否为表格起始（当前行含 |，下一行为分隔行）。"""
    if i + 1 >= len(lines):
        return False
    if '|' not in lines[i]:
        return False
    return bool(_TABLE_SEP_RE.match(lines[i + 1]))


def parse_blocks(text: str) -> List[Block]:
    """逐行扫描 Markdown 文本，识别并切分为原子块。"""
    lines = text.splitlines()
    blocks: List[Block] = []
    i = 0
    n = len(lines)

    while i < n:
        line = lines[i]
        stripped = line.strip()

        # 跳过纯空行（块间分隔）
        if not stripped:
            i += 1
            continue

        # 1) 围栏代码块（保持 ``` 闭合完整）
        fence_match = _FENCE_RE.match(stripped)
        if fence_match:
            fence = fence_match.group(1)  # ``` 或 ~~~
            buf = [line]
            i += 1
            closed = False
            while i < n:
                buf.append(lines[i])
                if lines[i].strip().startswith(fence):
                    i += 1
                    closed = True
                    break
                i += 1
            # 兜底：若未正常闭合，补一个 fence 保证产物的语法完整
            if not closed:
                buf.append(fence)
            blocks.append(Block(type='code', text='\n'.join(buf)))
            continue

        # 2) 标题
        h_match = _HEADING_RE.match(stripped)
        if h_match:
            level = len(h_match.group(1))
            blocks.append(Block(type='heading', text=line, level=level))
            i += 1
            continue

        # 3) 表格（标题行 + 分隔行 + 数据行，整表作为原子块）
        if _is_table_start(lines, i):
            buf = [line, lines[i + 1]]
            i += 2
            while i < n and lines[i].strip() and '|' in lines[i]:
                buf.append(lines[i])
                i += 1
            blocks.append(Block(type='table', text='\n'.join(buf)))
            continue

        # 4) 列表（连续列表项 + 列表项内的缩进续行 视为一个整体）
        if _LIST_RE.match(line):
            buf = [line]
            i += 1
            while i < n:
                cur = lines[i]
                if _LIST_RE.match(cur):
                    buf.append(cur)
                    i += 1
                elif cur.startswith(('  ', '\t')) and cur.strip():
                    # 缩进的续行属于上一个列表项
                    buf.append(cur)
                    i += 1
                elif not cur.strip():
                    # 空行：仅当下一行仍是列表项 / 缩进续行时认为列表未结束
                    if i + 1 < n and (
                        _LIST_RE.match(lines[i + 1])
                        or (lines[i + 1].startswith(('  ', '\t')) and lines[i + 1].strip())
                    ):
                        buf.append(cur)
                        i += 1
                    else:
                        break
                else:
                    break
            blocks.append(Block(type='list', text='\n'.join(buf)))
            continue

        # 5) 普通段落：直到空行 / 下一个特殊结构
        buf = [line]
        i += 1
        while i < n:
            cur = lines[i]
            if not cur.strip():
                break
            cur_strip = cur.strip()
            if _HEADING_RE.match(cur_strip):
                break
            if _FENCE_RE.match(cur_strip):
                break
            if _LIST_RE.match(cur):
                break
            if _is_table_start(lines, i):
                break
            buf.append(cur)
            i += 1
        blocks.append(Block(type='para', text='\n'.join(buf)))

    return blocks


# ============================================================
# 智能打包
# ============================================================

def _render(parts: List[Block]) -> str:
    """把若干原子块用空行拼接为最终 chunk 文本。"""
    return '\n\n'.join(p.text for p in parts).strip()


def _last_heading(parts: List[Block]) -> Optional[Block]:
    """取一组块中最后出现的标题（用作下一 chunk 的前缀）。"""
    last: Optional[Block] = None
    for p in parts:
        if p.type == 'heading':
            last = p
    return last


# ============================================================
# 上下文强化：标题面包屑前缀
# ============================================================

def _clean_heading_text(heading_line: str) -> str:
    """去除标题行的 # 标记，仅保留文字内容。"""
    m = _HEADING_RE.match(heading_line.strip())
    if m:
        return m.group(2).strip()
    return heading_line.strip()


def build_heading_prefix(
    heading_stack: List[Block],
    max_depth: int = 3,
    format: str = "breadcrumb",
) -> str:
    """根据标题栈生成 chunk 前缀字符串。

    参数：
        heading_stack: 当前标题栈（从顶层到深层）；Block.text 为原始标题行。
        max_depth: 最多传播的标题层级数（仅取栈尾 N 个标题）。
        format: 前缀格式，当前仅支持 "breadcrumb"。

    返回：
        面包屑前缀，形如 "[A > B > C]\n"；栈为空时返回 ""。
    """
    if not heading_stack or max_depth <= 0:
        return ""
    titles = [_clean_heading_text(h.text) for h in heading_stack[-max_depth:]]
    titles = [t for t in titles if t]
    if not titles:
        return ""
    # 仅支持 breadcrumb；其他格式默认 fallback 到 breadcrumb。
    return "[" + " > ".join(titles) + "]\n"


def chunk_markdown(
    text: str,
    min_tokens: int = 200,
    max_tokens: int = 400,
    context_prefix_enabled: bool = False,
    context_prefix_max_depth: int = 3,
    context_prefix_format: str = "breadcrumb",
    initial_heading_stack: Optional[List[Block]] = None,
) -> List[str]:
    """对 Markdown 文本进行结构感知分块。

    参数：
        text: Markdown 原文。
        min_tokens: 单 chunk 的目标下限（达到即可 flush）。
        max_tokens: 单 chunk 的目标上限（超过则提前 flush；
                    单个原子块本身超过该值时保持完整）。
        context_prefix_enabled: 是否在每个 chunk 文本前附加面包屑标题前缀。
        context_prefix_max_depth: 最多传播的标题层级数。
        context_prefix_format: 前缀格式（当前仅 breadcrumb）。
        initial_heading_stack: 外部传入的初始标题栈（用于层次化分块中 child
                    继承 parent 所处的标题上下文）。

    返回：
        分块后的字符串列表，已剔除空白 chunk。
        启用 context_prefix 后，每个 chunk 文本开头会附加面包屑标题前缀。
    """
    # 边界：空 / 纯空白
    if not text or not text.strip():
        return []

    blocks = parse_blocks(text)
    if not blocks:
        return []

    # 文本块 + 该 chunk flush 时的标题栈快照，用于最后生成面包屑前缀
    chunks_with_snap: List[Tuple[str, List[Block]]] = []
    cur: List[Block] = []
    cur_tokens = 0
    # 上一个 chunk 末尾的最近标题，用作下一 chunk 起始的上下文前缀
    pending_heading: Optional[Block] = None

    # 全局标题栈：随遍历推进同步更新，反映当前所处的标题层级路径
    heading_stack: List[Block] = list(initial_heading_stack or [])

    def _update_heading_stack(h: Block) -> None:
        # 同级或更高级标题会清除栈中所有 level >= h.level 的项
        while heading_stack and heading_stack[-1].level >= h.level:
            heading_stack.pop()
        heading_stack.append(h)

    def flush() -> None:
        """把当前累积的 cur 输出为一个 chunk，并更新 pending_heading。

        面包屑快照取 flush 时刻的 heading_stack，可完整反映 chunk 内容
        所在的标题层级（包含本 chunk 内出现的最深标题）。
        """
        nonlocal cur, cur_tokens, pending_heading
        if not cur:
            return
        chunks_with_snap.append((_render(cur), list(heading_stack)))
        last_h = _last_heading(cur)
        if last_h is not None:
            pending_heading = last_h
        cur = []
        cur_tokens = 0

    def inject_heading_prefix() -> None:
        """新 chunk 起始时若没有显式标题，注入 pending_heading 作为前缀。"""
        nonlocal cur_tokens, pending_heading
        if not cur and pending_heading is not None:
            cur.append(pending_heading)
            cur_tokens += estimate_tokens(pending_heading.text)
            pending_heading = None

    i = 0
    n = len(blocks)
    while i < n:
        b = blocks[i]
        tks = estimate_tokens(b.text)

        # ----- 标题：与后续合并，不单独成块 -----
        if b.type == 'heading':
            # 若当前累积已达 min，先 flush 让标题归入下一 chunk
            # 注意：此时 heading_stack 尚未更新，前一 chunk 的面包屑不会被本标题污染
            if cur_tokens >= min_tokens:
                flush()
            # 出现新的显式标题，无需再注入历史 pending_heading
            pending_heading = None
            cur.append(b)
            cur_tokens += tks
            _update_heading_stack(b)
            i += 1
            continue

        # ----- 单个原子块超长：保持完整，独立成 chunk -----
        if tks > max_tokens:
            # 当前累积中若已有正文（非全是标题），先 flush
            has_body = any(p.type != 'heading' for p in cur)
            if has_body:
                flush()
                # flush 后 cur 为空，此处可注入 pending_heading 作为大块前缀
                prefix: List[Block] = []
                if pending_heading is not None:
                    prefix.append(pending_heading)
                    pending_heading = None
                # 大块独立成 chunk：使用当前 heading_stack 作为面包屑上下文
                chunks_with_snap.append((_render(prefix + [b]), list(heading_stack)))
                # 大块输出后，沿用 prefix 中的标题作为后续 pending_heading
                last_h = _last_heading(prefix)
                if last_h is not None:
                    pending_heading = last_h
            else:
                # cur 仅含标题（或为空）：把这些标题作为大块的前缀一并输出
                prefix = list(cur)
                if not prefix and pending_heading is not None:
                    prefix.append(pending_heading)
                    pending_heading = None
                # 此时 heading_stack 已包含 cur 中的所有标题，面包屑反映完整层级
                chunks_with_snap.append((_render(prefix + [b]), list(heading_stack)))
                last_h = _last_heading(prefix)
                if last_h is not None:
                    pending_heading = last_h
                cur = []
                cur_tokens = 0
            i += 1
            continue

        # ----- 加入会超出 max：先 flush -----
        if cur and cur_tokens + tks > max_tokens:
            flush()

        # 新 chunk 起始：注入标题前缀
        inject_heading_prefix()

        cur.append(b)
        cur_tokens += tks
        i += 1

        # 达到 min 即 flush，后续靠标题前缀维持上下文连续性
        if cur_tokens >= min_tokens:
            flush()

    # 收尾
    if cur:
        chunks_with_snap.append((_render(cur), list(heading_stack)))

    # 统一应用面包屑前缀；关闭时行为与原逻辑完全等价
    result: List[str] = []
    for chunk_text, snap in chunks_with_snap:
        if context_prefix_enabled:
            prefix_str = build_heading_prefix(
                snap, context_prefix_max_depth, context_prefix_format
            )
            if prefix_str:
                chunk_text = prefix_str + chunk_text
        if chunk_text.strip():
            result.append(chunk_text)
    return result


# ============================================================
# Markdown 检测
# ============================================================

def looks_like_markdown(text: str) -> bool:
    """启发式检测：文本是否包含常见 Markdown 标志。

    检测信号（与需求规格一致）：
        - "\\n#"   标题行
        - "\\n```" 围栏代码块
        - "\\n|"   表格行
    在文本前补一个换行，以便首行也能命中信号。
    """
    if not text:
        return False
    probe = '\n' + text
    return ('\n#' in probe) or ('\n```' in probe) or ('\n|' in probe)

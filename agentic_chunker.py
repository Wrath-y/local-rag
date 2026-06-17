"""Agentic Chunking —— 基于 LLM 的智能分块。

设计要点：
- 不直接 import server / llm 模块；LLM 调用通过 ``llm_fn`` 注入，避免循环引用。
- 对超长文档按 ``max_llm_input_tokens`` 切分为多段（按行边界），逐段调用 LLM。
- LLM 返回的 JSON 形如：
    ``{"chunks": [{"start_line": 1, "end_line": 15, "summary": "..."}, ...]}``
  其中 line 索引为 1-based 的相对当前段行号。
- 错误降级：LLM 抛异常 / 返回非法 JSON / 返回边界异常 → 调用 ``fallback_fn(text, min_t, max_t)``，
  调用者通常传入结构感知分块作为兜底。
- 后处理：超过 ``max_tokens`` 的 LLM 块按行二次切分；过小的块与相邻合并；空块丢弃。
- ``generate_summary=True`` 时把 LLM 给出的 summary 以前缀形式写入 chunk 文本头部，
  形如 ``[摘要] xxx\\n``，与 markdown_chunker 的 breadcrumb 前缀机制风格一致。

仅依赖标准库（json / re / typing），不引入新外部依赖。
"""

from __future__ import annotations

import json
import re
from typing import Awaitable, Callable, Dict, List, Optional


# ============================================================
# Token 估算（与 server / markdown_chunker / semantic_chunker 一致）
# ============================================================

def _estimate_tokens(text: str) -> int:
    """估算文本 token 数：CJK 1 token/字，其余 4 字符/token（非 CJK 部分至少 1）。"""
    if not text:
        return 0
    cjk = sum(1 for c in text if '\u4e00' <= c <= '\u9fff')
    return cjk + max(1, (len(text) - cjk) // 4)


# ============================================================
# 默认 Prompt 构造
# ============================================================

# 默认中文 prompt 模板：要求 LLM 严格输出 JSON，便于解析。
DEFAULT_PROMPT_TEMPLATE = """你是一个文档分块专家。请分析以下文档并将其分成语义连贯的块。

要求：
1. 每个块应该是一个完整的语义单元（一个主题/概念/步骤）
2. 每个块大约 {min_tokens}-{max_tokens} 个 token（中文约 {min_tokens}-{max_tokens} 字）
3. 保持表格、代码块、列表等结构的完整性
4. 行号从 1 开始，使用文档下方标注的行号
5. {summary_clause}
6. 严格返回 JSON，不要输出任何解释、Markdown 代码围栏或额外文字。
   JSON 形如：
{{
  "chunks": [
    {{"start_line": 1, "end_line": 15{summary_field_example}}},
    {{"start_line": 16, "end_line": 30{summary_field_example}}}
  ]
}}

文档内容：
---
{numbered_text}
---"""


def _build_prompt(
    text: str,
    min_tokens: int,
    max_tokens: int,
    generate_summary: bool,
) -> str:
    """构造发送给 LLM 的 prompt；按行加上 1-based 行号便于 LLM 引用边界。"""
    lines = text.splitlines() or [text]
    numbered = "\n".join(f"{i + 1}: {ln}" for i, ln in enumerate(lines))
    if generate_summary:
        summary_clause = '为每个块给出一句 30 字以内的中文摘要，写在 "summary" 字段里。'
        summary_field_example = ', "summary": "本块要点的简短摘要"'
    else:
        summary_clause = '不需要生成摘要，可省略 "summary" 字段。'
        summary_field_example = ''
    return DEFAULT_PROMPT_TEMPLATE.format(
        min_tokens=min_tokens,
        max_tokens=max_tokens,
        summary_clause=summary_clause,
        summary_field_example=summary_field_example,
        numbered_text=numbered,
    )


# ============================================================
# JSON 解析（容错）
# ============================================================

# 匹配 ```json ... ``` 或 ``` ... ``` 围栏，便于剥离 LLM 的多余包裹。
_FENCE_RE = re.compile(r"```(?:json)?\s*(.*?)```", re.DOTALL | re.IGNORECASE)


def _extract_json(raw: str) -> Optional[dict]:
    """从 LLM 原文中尽力提取 JSON 对象；失败返回 None。"""
    if not raw:
        return None
    candidates: List[str] = []
    # 1) 直接尝试整段
    candidates.append(raw.strip())
    # 2) 剥离 ``` 围栏
    for m in _FENCE_RE.finditer(raw):
        candidates.append(m.group(1).strip())
    # 3) 截取首个 '{' 到最后一个 '}' 之间的子串
    lb = raw.find("{")
    rb = raw.rfind("}")
    if lb != -1 and rb != -1 and rb > lb:
        candidates.append(raw[lb:rb + 1])

    for c in candidates:
        try:
            obj = json.loads(c)
            if isinstance(obj, dict):
                return obj
        except Exception:
            continue
    return None


# ============================================================
# 边界归一化与切片
# ============================================================

def _normalize_boundaries(
    raw_chunks: List[Dict],
    n_lines: int,
) -> List[Dict]:
    """把 LLM 返回的 chunk 列表归一化为合法、连续、不交叉的边界。

    - 限制 start_line / end_line 在 [1, n_lines]；start <= end。
    - 按 start_line 排序后修正重叠：后段的 start <= 前段 end 时，把当前段抬升到 end+1。
    - 修补缺口：若某段 start > 上段 end + 1，则把上段 end 扩到 start - 1（避免行丢失）。
    - 首段强制从 1 开始，末段强制到 n_lines 结束（兜底覆盖）。
    """
    cleaned: List[Dict] = []
    for ch in raw_chunks:
        if not isinstance(ch, dict):
            continue
        try:
            s = int(ch.get("start_line"))
            e = int(ch.get("end_line"))
        except (TypeError, ValueError):
            continue
        s = max(1, min(n_lines, s))
        e = max(1, min(n_lines, e))
        if e < s:
            s, e = e, s
        cleaned.append({
            "start_line": s,
            "end_line": e,
            "summary": str(ch.get("summary") or "").strip() if ch.get("summary") else "",
        })

    if not cleaned:
        return []

    # 排序 + 去重叠
    cleaned.sort(key=lambda x: (x["start_line"], x["end_line"]))
    fixed: List[Dict] = []
    prev_end = 0
    for ch in cleaned:
        if ch["start_line"] <= prev_end:
            ch["start_line"] = prev_end + 1
        if ch["end_line"] < ch["start_line"]:
            continue
        fixed.append(ch)
        prev_end = ch["end_line"]

    if not fixed:
        return []

    # 修补首尾缺口
    if fixed[0]["start_line"] > 1:
        fixed[0]["start_line"] = 1
    if fixed[-1]["end_line"] < n_lines:
        fixed[-1]["end_line"] = n_lines

    # 修补中间缺口
    for i in range(len(fixed) - 1):
        if fixed[i]["end_line"] + 1 < fixed[i + 1]["start_line"]:
            fixed[i]["end_line"] = fixed[i + 1]["start_line"] - 1

    return fixed


def _slice_lines(lines: List[str], start: int, end: int) -> str:
    """提取 1-based 闭区间 [start, end] 的文本。"""
    s_idx = max(0, start - 1)
    e_idx = min(len(lines), end)
    return "\n".join(lines[s_idx:e_idx])


# ============================================================
# 后处理：超长 / 过小 chunk
# ============================================================

def _split_oversize_text(text: str, max_tokens: int) -> List[str]:
    """单个 chunk 超过 max_tokens 时按行二次切分；至少返回 1 段。"""
    if _estimate_tokens(text) <= max_tokens:
        return [text]
    out: List[str] = []
    cur: List[str] = []
    cur_tk = 0
    for ln in text.splitlines():
        tk = _estimate_tokens(ln)
        if cur and cur_tk + tk > max_tokens:
            out.append("\n".join(cur))
            cur = [ln]
            cur_tk = tk
        else:
            cur.append(ln)
            cur_tk += tk
    if cur:
        out.append("\n".join(cur))
    return [c for c in out if c.strip()] or [text]


def _merge_undersize(
    items: List[Dict],
    min_tokens: int,
    max_tokens: int,
) -> List[Dict]:
    """把 token 数小于 min_tokens 的 chunk 与相邻合并；优先合并到右侧。

    items 形如 [{"text": str, "summary": str}, ...]
    """
    if not items:
        return items
    changed = True
    while changed:
        changed = False
        new_items: List[Dict] = []
        i = 0
        while i < len(items):
            cur = items[i]
            tk = _estimate_tokens(cur["text"])
            if tk < min_tokens:
                # 尝试与右侧合并
                if i + 1 < len(items) and tk + _estimate_tokens(items[i + 1]["text"]) <= max_tokens:
                    nxt = items[i + 1]
                    merged_text = (cur["text"] + "\n" + nxt["text"]).strip()
                    merged_summary = "; ".join(s for s in (cur["summary"], nxt["summary"]) if s)
                    new_items.append({"text": merged_text, "summary": merged_summary})
                    i += 2
                    changed = True
                    continue
                # 尝试与左侧合并
                if new_items and tk + _estimate_tokens(new_items[-1]["text"]) <= max_tokens:
                    prev = new_items[-1]
                    merged_text = (prev["text"] + "\n" + cur["text"]).strip()
                    merged_summary = "; ".join(s for s in (prev["summary"], cur["summary"]) if s)
                    new_items[-1] = {"text": merged_text, "summary": merged_summary}
                    i += 1
                    changed = True
                    continue
            new_items.append(cur)
            i += 1
        items = new_items
    return items


# ============================================================
# 文档分段（控制单次 LLM 输入 token 上限）
# ============================================================

def _segment_text(text: str, max_input_tokens: int) -> List[str]:
    """按 max_input_tokens 把整篇文档分段；按行边界切，避免拆断单行。

    - 单行超过 max_input_tokens 仍独立成段（保证不丢内容）。
    - 段尽量贴近上限，减少 LLM 调用次数。
    """
    if max_input_tokens <= 0 or _estimate_tokens(text) <= max_input_tokens:
        return [text]
    out: List[str] = []
    cur: List[str] = []
    cur_tk = 0
    for ln in text.splitlines():
        tk = _estimate_tokens(ln) + 1  # +1 估算换行
        if cur and cur_tk + tk > max_input_tokens:
            out.append("\n".join(cur))
            cur = [ln]
            cur_tk = tk
        else:
            cur.append(ln)
            cur_tk += tk
    if cur:
        out.append("\n".join(cur))
    return [s for s in out if s.strip()] or [text]


# ============================================================
# 核心：单段 agentic 分块
# ============================================================

async def _chunk_segment(
    segment: str,
    llm_fn: Callable[[str], Awaitable[str]],
    generate_summary: bool,
    min_tokens: int,
    max_tokens: int,
    log_fn: Callable[[str], None],
) -> Optional[List[Dict]]:
    """对单段调用 LLM，解析返回的 JSON，渲染为 [{"text", "summary"}] 列表。

    返回 None 表示本段 LLM 失败（调用者应整体降级）。
    """
    lines = segment.splitlines() or [segment]
    n = len(lines)

    prompt = _build_prompt(segment, min_tokens, max_tokens, generate_summary)
    try:
        raw = await llm_fn(prompt)
    except Exception as e:
        log_fn(f"[agentic] LLM call failed: {e}")
        return None

    obj = _extract_json(raw or "")
    if not obj or "chunks" not in obj or not isinstance(obj["chunks"], list):
        log_fn(f"[agentic] invalid JSON from LLM, raw head={ (raw or '')[:200]!r}")
        return None

    fixed = _normalize_boundaries(obj["chunks"], n)
    if not fixed:
        log_fn("[agentic] no valid boundaries after normalization")
        return None

    items: List[Dict] = []
    for ch in fixed:
        text = _slice_lines(lines, ch["start_line"], ch["end_line"])
        if not text.strip():
            continue
        # 单块超大 → 按行二次切分；摘要仅挂在第一段，避免重复
        pieces = _split_oversize_text(text, max_tokens)
        for k, p in enumerate(pieces):
            items.append({
                "text": p,
                "summary": ch["summary"] if k == 0 else "",
            })

    if not items:
        return None
    return items


# ============================================================
# 主入口
# ============================================================

async def agentic_chunk(
    text: str,
    llm_fn: Callable[[str], Awaitable[str]],
    generate_summary: bool = True,
    max_llm_input_tokens: int = 4000,
    min_tokens: int = 200,
    max_tokens: int = 400,
    fallback_fn: Optional[Callable[[str, int, int], List[str]]] = None,
    log_fn: Optional[Callable[[str], None]] = None,
) -> List[str]:
    """使用 LLM 智能分块，返回 chunk 字符串列表。

    参数：
        text: 原文。
        llm_fn: 异步 LLM 调用函数；接收 prompt 字符串，返回 LLM 文本响应。
                由调用方注入（基于项目 llm/ 目录下的 provider）。
        generate_summary: 是否要求 LLM 为每个 chunk 生成摘要并以前缀形式附加到文本前。
        max_llm_input_tokens: 单次发给 LLM 的最大 token 数；超长文档按行分段调用。
        min_tokens / max_tokens: chunk 大小目标区间，用于 prompt 引导和后处理修正。
        fallback_fn: LLM 调用失败 / 返回非法格式时的兜底分块函数；
                     签名 ``(text, min_tokens, max_tokens) -> List[str]``。
                     建议传入结构感知分块（structure）。
        log_fn: 日志函数，默认 print；测试中可注入收集器。

    返回：
        chunk 字符串列表；空文本返回 []。

    错误处理：
        - LLM 异常 / JSON 解析失败 / 边界异常 → 调用 fallback_fn；
          fallback_fn 也失败或为 None 时返回 [text]（最后兜底，避免数据丢失）。
    """
    if log_fn is None:
        log_fn = print  # type: ignore[assignment]

    # 边界 1：空文本
    if not text or not text.strip():
        return []

    def _fallback(reason: str) -> List[str]:
        log_fn(f"[agentic] fallback to structure: {reason}")
        if fallback_fn is None:
            return [text]
        try:
            out = fallback_fn(text, min_tokens, max_tokens)
            return out if out else [text]
        except Exception as e:
            log_fn(f"[agentic] fallback function raised: {e}")
            return [text]

    # 切段（控制 LLM 输入上限）；逐段调用 LLM
    segments = _segment_text(text, max_llm_input_tokens)
    log_fn(f"[agentic] split into {len(segments)} segment(s) for LLM")

    all_items: List[Dict] = []
    for idx, seg in enumerate(segments):
        items = await _chunk_segment(
            seg, llm_fn, generate_summary, min_tokens, max_tokens, log_fn
        )
        if items is None:
            # 任一段失败 → 整体降级，避免一半文档走 agentic 一半走 fallback 造成不一致
            return _fallback(f"segment {idx + 1}/{len(segments)} failed")
        all_items.extend(items)

    if not all_items:
        return _fallback("no chunks produced")

    # 合并过小块
    all_items = _merge_undersize(all_items, min_tokens, max_tokens)

    # 渲染：generate_summary=True 时把 summary 以前缀形式贴到 chunk 头部
    out: List[str] = []
    for it in all_items:
        ct = it["text"]
        if generate_summary and it.get("summary"):
            ct = f"[摘要] {it['summary']}\n{ct}"
        if ct.strip():
            out.append(ct)
    return out

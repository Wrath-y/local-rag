"""语义分块器（Semantic Chunking）。

基于 embedding 模型计算相邻句子的余弦相似度，在语义断裂处（相似度骤降）切分，
使每个 chunk 内部保持语义连贯性。

设计要点：
- 不直接 import server 模块，encode 函数通过参数注入，避免循环引用。
- 仅依赖 numpy（项目已有），不引入新外部依赖。
- 句子分割与 server.py 中 _chunk_plain_text 保持一致的标点规则。
- 处理边界情况：文本太短（< 3 句）直接返回整段；句子数不足以计算分位数时退化为不切分。
- 后处理：对句子数过大的 chunk 二次均匀切分；对过小的 chunk 与相邻 chunk 合并；
  并按 token 估算保证最终输出的尺寸大致落在 [min_tokens, max_tokens] 之间。
"""

from __future__ import annotations

import re
from typing import Callable, List, Sequence

import numpy as np


# ============================================================
# Token 估算（与 server._chunk_plain_text / markdown_chunker 保持一致）
# ============================================================

def _estimate_tokens(text: str) -> int:
    """估算文本 token 数：CJK 1 token/字，其余 4 字符/token（至少 1）。"""
    if not text:
        return 0
    cjk = sum(1 for c in text if '\u4e00' <= c <= '\u9fff')
    return cjk + max(1, (len(text) - cjk) // 4)


# ============================================================
# 句子分割
# ============================================================

# 中英混合句末标点 + 换行作为分割点（保持与 server._chunk_plain_text 一致）
_SENT_SPLIT_RE = re.compile(r'(?<=[。！？.!?\n])\s*')


def split_sentences(text: str) -> List[str]:
    """将文本按句末标点切分为句子列表，去除空白条目。"""
    parts = _SENT_SPLIT_RE.split(text)
    return [p.strip() for p in parts if p and p.strip()]


# ============================================================
# 相似度计算
# ============================================================

def _adjacent_cosine(embeddings: np.ndarray) -> np.ndarray:
    """计算相邻句子两两余弦相似度。

    输入 embeddings 形状 (n, d)；输出形状 (n-1,)。
    若 encode_fn 已返回归一化向量则直接点积；否则做一次归一化以确保结果在 [-1, 1]。
    """
    if embeddings.shape[0] < 2:
        return np.zeros(0, dtype=np.float32)
    a = embeddings[:-1]
    b = embeddings[1:]
    # 归一化兜底：即便上游已 normalize，再做一次也不会改变结果
    a_norm = np.linalg.norm(a, axis=1, keepdims=True)
    b_norm = np.linalg.norm(b, axis=1, keepdims=True)
    a_norm[a_norm == 0] = 1.0
    b_norm[b_norm == 0] = 1.0
    a = a / a_norm
    b = b / b_norm
    return np.sum(a * b, axis=1).astype(np.float32)


# ============================================================
# 后处理：拆大块 / 合并小块
# ============================================================

def _split_oversize(group: List[int], max_chunk_size: int) -> List[List[int]]:
    """将句子索引组按 max_chunk_size 上限做均匀二次切分。"""
    if len(group) <= max_chunk_size:
        return [group]
    out: List[List[int]] = []
    i = 0
    while i < len(group):
        out.append(group[i:i + max_chunk_size])
        i += max_chunk_size
    return out


def _merge_undersize(
    groups: List[List[int]],
    min_chunk_size: int,
    max_chunk_size: int,
) -> List[List[int]]:
    """对句子数小于 min_chunk_size 的 chunk 与相邻 chunk 合并。

    合并优先：先尝试与右侧合并，若右侧合并后仍不超过 max_chunk_size 则合并；
    否则与左侧合并；若两侧都会超限，则保留原状（罕见，仅在两侧都接近 max 时）。
    """
    if not groups:
        return groups
    # 反复扫描直到不再发生合并
    changed = True
    while changed:
        changed = False
        i = 0
        new_groups: List[List[int]] = []
        while i < len(groups):
            cur = groups[i]
            if len(cur) < min_chunk_size:
                # 尝试合并到右侧
                if i + 1 < len(groups) and len(cur) + len(groups[i + 1]) <= max_chunk_size:
                    merged = cur + groups[i + 1]
                    new_groups.append(merged)
                    i += 2
                    changed = True
                    continue
                # 尝试合并到左侧（已 append 的最后一组）
                if new_groups and len(new_groups[-1]) + len(cur) <= max_chunk_size:
                    new_groups[-1] = new_groups[-1] + cur
                    i += 1
                    changed = True
                    continue
            new_groups.append(cur)
            i += 1
        groups = new_groups
    return groups


# ============================================================
# 按 token 上限对 chunk 文本兜底拆分
# ============================================================

def _enforce_token_limits(
    sentences: Sequence[str],
    groups: List[List[int]],
    min_tokens: int,
    max_tokens: int,
) -> List[str]:
    """将句子组渲染为 chunk 文本；若单组超出 max_tokens 则按句子顺序二次切分。

    渲染规则与 server._chunk_plain_text 保持一致：用空字符串拼接（句末标点已保留）。
    """
    out: List[str] = []
    for grp in groups:
        if not grp:
            continue
        # 单组超 max_tokens：按 token 累加切分
        cur: List[str] = []
        cur_tk = 0
        for idx in grp:
            s = sentences[idx]
            tk = _estimate_tokens(s)
            if cur and cur_tk + tk > max_tokens:
                out.append("".join(cur))
                cur = [s]
                cur_tk = tk
            else:
                cur.append(s)
                cur_tk += tk
        if cur:
            text = "".join(cur)
            if text.strip():
                out.append(text)
    # min_tokens 仅作软约束，避免与 min_chunk_size 互相打架；此处不强行合并
    _ = min_tokens
    return out


# ============================================================
# 主入口
# ============================================================

def semantic_chunk(
    text: str,
    encode_fn: Callable[[List[str]], np.ndarray],
    threshold_percentile: int = 90,
    min_chunk_size: int = 2,
    max_chunk_size: int = 20,
    min_tokens: int = 200,
    max_tokens: int = 400,
) -> List[str]:
    """基于语义相似度的文档分块。

    算法步骤：
        1. 句子分割。
        2. 调用 ``encode_fn`` 对所有句子批量生成 embedding。
        3. 计算相邻句子余弦相似度数组。
        4. 取相似度的第 ``(100 - threshold_percentile)`` 百分位作为切分阈值，
           即"低于该值的位置"被视为语义断裂；等价于"取最低的 (100 - p)% 处切分"。
        5. 在切分点处分块，得到初始句子组。
        6. 对超 ``max_chunk_size`` 句的组做均匀二次切分。
        7. 对小于 ``min_chunk_size`` 句的组与相邻组合并。
        8. 渲染为字符串；若单组渲染后超 ``max_tokens`` 则按句子二次切分。

    参数：
        text: 原文。
        encode_fn: 接收 ``List[str]``、返回形状 ``(n, dim)`` 的 numpy 数组的编码函数。
                   通常由调用方传入 ``model.encode``（建议 normalize_embeddings=True）。
        threshold_percentile: 相似度断裂阈值百分位，越小切分越细；推荐 80~95。
        min_chunk_size: 最小 chunk 句子数。
        max_chunk_size: 最大 chunk 句子数。
        min_tokens: token 软下限（仅参考，不强制合并）。
        max_tokens: token 硬上限，超出会触发二次切分。

    返回：
        分块后的字符串列表。空文本或纯空白返回 []。
    """
    # 边界 1：空文本
    if not text or not text.strip():
        return []

    sentences = split_sentences(text)
    n = len(sentences)

    # 边界 2：少于 3 句直接整段返回（无法形成有意义的语义断裂判定）
    if n < 3:
        joined = "".join(sentences)
        return [joined] if joined.strip() else []

    # 计算 embedding；encode_fn 异常或形状异常时降级为整段返回
    try:
        embeddings = np.asarray(encode_fn(sentences), dtype=np.float32)
    except Exception:
        return ["".join(sentences)]

    if embeddings.ndim != 2 or embeddings.shape[0] != n:
        return ["".join(sentences)]

    sims = _adjacent_cosine(embeddings)  # 形状 (n-1,)
    if sims.size == 0:
        return ["".join(sentences)]

    # 边界 3：相邻相似度样本太少时（< 4 个），不足以稳定计算分位数，退化为不切分
    if sims.size < 4:
        groups: List[List[int]] = [list(range(n))]
    else:
        # 取低于第 (100 - p) 百分位的位置作为切分点
        # 例：p=90 → 取最低的 10% 相似度位置作为断裂点
        p = max(1, min(99, int(threshold_percentile)))
        cut_threshold = float(np.percentile(sims, 100 - p))

        cut_points: List[int] = []
        for i, s in enumerate(sims):
            # i 表示句子 i 与句子 i+1 之间的相似度；切点放在 i+1 之前
            if s <= cut_threshold:
                cut_points.append(i + 1)

        # 组装句子组
        groups = []
        prev = 0
        for cp in cut_points:
            if cp > prev:
                groups.append(list(range(prev, cp)))
                prev = cp
        if prev < n:
            groups.append(list(range(prev, n)))
        if not groups:
            groups = [list(range(n))]

    # 拆大块 → 合并小块
    expanded: List[List[int]] = []
    for g in groups:
        expanded.extend(_split_oversize(g, max_chunk_size))
    groups = _merge_undersize(expanded, min_chunk_size, max_chunk_size)

    # 渲染并按 token 上限兜底
    return _enforce_token_limits(sentences, groups, min_tokens, max_tokens)

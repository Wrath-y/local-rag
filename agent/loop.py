import json
import os

import yaml

from llm.factory import get_provider
from .memory import append_message, load_history
from .tools import TOOLS, execute_tool

_cfg_path = os.path.join(os.path.dirname(__file__), "..", "config.yaml")
with open(_cfg_path) as _f:
    _cfg = yaml.safe_load(_f)

_llm = get_provider(_cfg["llm"])

MAX_TOOL_ROUNDS = 5

_SYSTEM_PROMPT = (
    "You are a helpful assistant with access to a local knowledge base via RAG tools. "
    "Use rag_retrieve when the user asks questions that might be answered by the knowledge base. "
    "Use other tools as appropriate. Answer in the same language the user writes in."
)


def _anthropic_tools(tools: list[dict]) -> list[dict]:
    """Convert OpenAI function format to Anthropic tool format."""
    result = []
    for t in tools:
        result.append({
            "name": t["name"],
            "description": t["description"],
            "input_schema": t["parameters"],
        })
    return result


async def run(session_id: str, user_input: str) -> str:
    append_message(session_id, "user", user_input)
    history = load_history(session_id)

    provider_name = _cfg["llm"].get("provider", "anthropic")

    for _ in range(MAX_TOOL_ROUNDS):
        if provider_name == "anthropic":
            response = await _llm.complete(
                [{"role": "system", "content": _SYSTEM_PROMPT}] + history,
                tools=_anthropic_tools(TOOLS),
                tool_choice={"type": "auto"},
            )
        else:
            response = await _llm.complete(
                [{"role": "system", "content": _SYSTEM_PROMPT}] + history,
                tools=[{"type": "function", "function": t} for t in TOOLS],
                tool_choice="auto",
            )

        # Plain text reply → done
        if isinstance(response, str):
            append_message(session_id, "assistant", response)
            return response

        # Tool call (dict with tool_calls or content blocks)
        tool_name, tool_args = _extract_tool_call(response, provider_name)
        if tool_name is None:
            # No tool call found, treat as plain text
            text = _extract_text(response, provider_name)
            append_message(session_id, "assistant", text)
            return text

        tool_result = await execute_tool(tool_name, tool_args)
        append_message(
            session_id,
            "tool",
            json.dumps({"tool": tool_name, "args": tool_args, "result": tool_result}),
        )
        history = load_history(session_id)

    fallback = "Reached maximum tool call rounds. Please rephrase your question."
    append_message(session_id, "assistant", fallback)
    return fallback


def _extract_tool_call(response, provider: str) -> tuple[str | None, dict]:
    """Extract (tool_name, args) from a provider response dict, or (None, {})."""
    if provider == "anthropic":
        # Anthropic returns a list of content blocks
        if isinstance(response, list):
            for block in response:
                if isinstance(block, dict) and block.get("type") == "tool_use":
                    return block["name"], block.get("input", {})
    else:
        # OpenAI returns a message dict with tool_calls
        if isinstance(response, dict):
            calls = response.get("tool_calls") or []
            if calls:
                fn = calls[0]["function"]
                return fn["name"], json.loads(fn.get("arguments", "{}"))
    return None, {}


def _extract_text(response, provider: str) -> str:
    if provider == "anthropic" and isinstance(response, list):
        for block in response:
            if isinstance(block, dict) and block.get("type") == "text":
                return block.get("text", "")
    if isinstance(response, dict):
        return response.get("content", "") or ""
    return str(response)

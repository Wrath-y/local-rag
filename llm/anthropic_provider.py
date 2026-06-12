import os
from typing import AsyncIterator

from .base import LLMProvider


class AnthropicProvider(LLMProvider):
    def __init__(self, cfg: dict):
        from anthropic import AsyncAnthropic
        self._client = AsyncAnthropic(
            api_key=os.environ[cfg["api_key_env"]],
            timeout=cfg.get("timeout", 30),
            max_retries=cfg.get("max_retries", 2),
        )
        self._model = cfg["model"]

    def _split_messages(self, messages: list[dict]) -> tuple[str | None, list[dict]]:
        # Anthropic treats 'system' as a top-level field, not a message role
        system = next((m["content"] for m in messages if m["role"] == "system"), None)
        turns = [m for m in messages if m["role"] != "system"]
        return system, turns

    async def complete(self, messages: list[dict], **kwargs) -> str:
        system, turns = self._split_messages(messages)
        params = dict(
            model=self._model,
            max_tokens=kwargs.pop("max_tokens", 4096),
            messages=turns,
            **kwargs,
        )
        if system:
            params["system"] = system
        from anthropic import AsyncAnthropic  # noqa: F401 – already imported at module level
        resp = await self._client.messages.create(**params)
        return resp.content[0].text

    async def stream(self, messages: list[dict], **kwargs) -> AsyncIterator[str]:
        system, turns = self._split_messages(messages)
        params = dict(
            model=self._model,
            max_tokens=kwargs.pop("max_tokens", 4096),
            messages=turns,
            **kwargs,
        )
        if system:
            params["system"] = system
        async with self._client.messages.stream(**params) as s:
            async for text in s.text_stream:
                yield text

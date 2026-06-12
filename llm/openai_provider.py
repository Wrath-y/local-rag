import os
from typing import AsyncIterator

from .base import LLMProvider


class OpenAIProvider(LLMProvider):
    def __init__(self, cfg: dict):
        from openai import AsyncOpenAI
        self._client = AsyncOpenAI(
            api_key=os.environ[cfg["api_key_env"]],
            timeout=cfg.get("timeout", 30),
            max_retries=cfg.get("max_retries", 2),
        )
        self._model = cfg["model"]

    async def complete(self, messages: list[dict], **kwargs) -> str:
        resp = await self._client.chat.completions.create(
            model=self._model, messages=messages, **kwargs
        )
        return resp.choices[0].message.content

    async def stream(self, messages: list[dict], **kwargs) -> AsyncIterator[str]:
        async with self._client.chat.completions.stream(
            model=self._model, messages=messages, **kwargs
        ) as s:
            async for chunk in s:
                delta = chunk.choices[0].delta.content
                if delta:
                    yield delta

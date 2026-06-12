import pytest
from unittest.mock import AsyncMock, MagicMock, patch


@pytest.fixture(autouse=True)
def fake_env(monkeypatch):
    monkeypatch.setenv("OPENAI_API_KEY", "sk-test-openai")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-test-anthropic")


# ── AnthropicProvider ────────────────────────────────────────────────────────

class TestAnthropicProvider:
    def _make(self):
        with patch("anthropic.AsyncAnthropic"):
            from llm.anthropic_provider import AnthropicProvider
            return AnthropicProvider({
                "model": "claude-sonnet-4-6",
                "api_key_env": "ANTHROPIC_API_KEY",
            })

    def test_split_messages_extracts_system(self):
        p = self._make()
        msgs = [
            {"role": "system", "content": "You are helpful."},
            {"role": "user", "content": "Hello"},
        ]
        system, turns = p._split_messages(msgs)
        assert system == "You are helpful."
        assert len(turns) == 1
        assert turns[0]["role"] == "user"

    def test_split_messages_no_system(self):
        p = self._make()
        msgs = [{"role": "user", "content": "Hi"}]
        system, turns = p._split_messages(msgs)
        assert system is None
        assert turns == msgs

    @pytest.mark.asyncio
    async def test_complete(self):
        p = self._make()
        mock_resp = MagicMock()
        mock_resp.content = [MagicMock(text="answer")]
        p._client.messages.create = AsyncMock(return_value=mock_resp)

        result = await p.complete([{"role": "user", "content": "Hi"}])
        assert result == "answer"
        p._client.messages.create.assert_called_once()
        call_kwargs = p._client.messages.create.call_args.kwargs
        assert call_kwargs["model"] == "claude-sonnet-4-6"
        assert "system" not in call_kwargs  # no system message in input

    @pytest.mark.asyncio
    async def test_complete_with_system(self):
        p = self._make()
        mock_resp = MagicMock()
        mock_resp.content = [MagicMock(text="ok")]
        p._client.messages.create = AsyncMock(return_value=mock_resp)

        msgs = [
            {"role": "system", "content": "Be brief."},
            {"role": "user", "content": "Hi"},
        ]
        await p.complete(msgs)
        call_kwargs = p._client.messages.create.call_args.kwargs
        assert call_kwargs["system"] == "Be brief."
        assert all(m["role"] != "system" for m in call_kwargs["messages"])

    @pytest.mark.asyncio
    async def test_stream(self):
        p = self._make()

        async def fake_text_stream():
            for token in ["hel", "lo"]:
                yield token

        mock_stream_ctx = MagicMock()
        mock_stream_ctx.__aenter__ = AsyncMock(return_value=mock_stream_ctx)
        mock_stream_ctx.__aexit__ = AsyncMock(return_value=False)
        mock_stream_ctx.text_stream = fake_text_stream()
        p._client.messages.stream = MagicMock(return_value=mock_stream_ctx)

        tokens = []
        async for t in p.stream([{"role": "user", "content": "Hi"}]):
            tokens.append(t)
        assert tokens == ["hel", "lo"]


# ── OpenAIProvider ───────────────────────────────────────────────────────────

class TestOpenAIProvider:
    def _make(self):
        with patch("openai.AsyncOpenAI"):
            from llm.openai_provider import OpenAIProvider
            return OpenAIProvider({
                "model": "gpt-4o",
                "api_key_env": "OPENAI_API_KEY",
            })

    @pytest.mark.asyncio
    async def test_complete(self):
        p = self._make()
        mock_choice = MagicMock()
        mock_choice.message.content = "response text"
        mock_resp = MagicMock()
        mock_resp.choices = [mock_choice]
        p._client.chat.completions.create = AsyncMock(return_value=mock_resp)

        result = await p.complete([{"role": "user", "content": "Hi"}])
        assert result == "response text"
        call_kwargs = p._client.chat.completions.create.call_args.kwargs
        assert call_kwargs["model"] == "gpt-4o"

import os
import pytest
from unittest.mock import patch


@pytest.fixture(autouse=True)
def fake_env(monkeypatch):
    monkeypatch.setenv("OPENAI_API_KEY", "sk-test-openai")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-test-anthropic")


def test_get_provider_anthropic():
    with patch("anthropic.AsyncAnthropic"):
        from llm import get_provider
        from llm.anthropic_provider import AnthropicProvider
        p = get_provider({"provider": "anthropic", "model": "claude-sonnet-4-6",
                          "api_key_env": "ANTHROPIC_API_KEY"})
        assert isinstance(p, AnthropicProvider)


def test_get_provider_openai():
    with patch("openai.AsyncOpenAI"):
        from llm import get_provider
        from llm.openai_provider import OpenAIProvider
        p = get_provider({"provider": "openai", "model": "gpt-4o",
                          "api_key_env": "OPENAI_API_KEY"})
        assert isinstance(p, OpenAIProvider)


def test_get_provider_unknown():
    from llm import get_provider
    with pytest.raises(ValueError, match="Unknown LLM provider"):
        get_provider({"provider": "nonexistent", "model": "x",
                      "api_key_env": "OPENAI_API_KEY"})


def test_get_provider_default_is_anthropic():
    with patch("anthropic.AsyncAnthropic"):
        from llm import get_provider
        from llm.anthropic_provider import AnthropicProvider
        # no "provider" key → defaults to "anthropic"
        p = get_provider({"model": "claude-sonnet-4-6", "api_key_env": "ANTHROPIC_API_KEY"})
        assert isinstance(p, AnthropicProvider)


def test_custom_provider_registration():
    from llm.factory import register, get_provider
    from llm.base import LLMProvider

    class FakeProvider(LLMProvider):
        def __init__(self, cfg): pass
        async def complete(self, messages, **kw): return ""
        async def stream(self, messages, **kw):
            yield ""

    register("fake")(FakeProvider)
    p = get_provider({"provider": "fake", "model": "x", "api_key_env": "OPENAI_API_KEY"})
    assert isinstance(p, FakeProvider)

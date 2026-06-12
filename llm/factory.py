from .base import LLMProvider

_REGISTRY: dict[str, type] = {}


def register(name: str):
    def decorator(cls: type) -> type:
        _REGISTRY[name] = cls
        return cls
    return decorator


def get_provider(cfg: dict) -> LLMProvider:
    name = cfg.get("provider", "anthropic")
    cls = _REGISTRY.get(name)
    if cls is None:
        available = list(_REGISTRY)
        raise ValueError(
            f"Unknown LLM provider: {name!r}. Available: {available}"
        )
    return cls(cfg)


# Register built-in providers
from .openai_provider import OpenAIProvider        # noqa: E402
from .anthropic_provider import AnthropicProvider  # noqa: E402

register("openai")(OpenAIProvider)
register("anthropic")(AnthropicProvider)

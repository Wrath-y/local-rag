"""Lightweight embedding + rerank sidecar. OpenAI-compatible API format."""
import argparse
from fastapi import FastAPI
from pydantic import BaseModel
from typing import List, Optional

app = FastAPI()
_models = {}


def _get_embed_model(name: str):
    if name not in _models:
        from sentence_transformers import SentenceTransformer
        _models[name] = SentenceTransformer(name, cache_folder="sidecar/models")
    return _models[name]


def _get_rerank_model(name: str):
    key = f"rerank:{name}"
    if key not in _models:
        from sentence_transformers import CrossEncoder
        _models[key] = CrossEncoder(name)
    return _models[key]


class EmbedRequest(BaseModel):
    input: List[str]
    model: str = "BAAI/bge-small-zh-v1.5"


class RerankRequest(BaseModel):
    query: str
    documents: List[str]
    model: str = "BAAI/bge-reranker-base"
    top_n: Optional[int] = None


@app.post("/embed")
def embed(req: EmbedRequest):
    model = _get_embed_model(req.model)
    embeddings = model.encode(req.input, normalize_embeddings=True).tolist()
    data = [{"index": i, "embedding": e} for i, e in enumerate(embeddings)]
    return {"data": data}


@app.post("/rerank")
def rerank(req: RerankRequest):
    model = _get_rerank_model(req.model)
    pairs = [(req.query, doc) for doc in req.documents]
    scores = model.predict(pairs).tolist()
    results = [{"index": i, "relevance_score": float(s)} for i, s in enumerate(scores)]
    results.sort(key=lambda x: x["relevance_score"], reverse=True)
    if req.top_n:
        results = results[:req.top_n]
    return {"results": results}


@app.get("/health")
def health():
    return {"status": "ok"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=8766)
    args = parser.parse_args()
    import uvicorn
    uvicorn.run(app, host="127.0.0.1", port=args.port, log_level="warning")

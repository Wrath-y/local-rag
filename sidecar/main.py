"""本地嵌入与重排服务：只加载已准备好的模型，普通运行始终离线。"""
import argparse
import os
from pathlib import Path
import signal
import sys
import threading
import time
from typing import List, Optional

# 必须在加载模型库前固定离线模式，环境中的相反设置不能触发隐式下载。
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["TRANSFORMERS_OFFLINE"] = "1"
os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

app = FastAPI()
_models = {}
_configured = {}
_ready = False
_instance = os.environ.get("LOCAL_RAG_SIDECAR_TOKEN", "")
_model_dir = str(Path(__file__).resolve().parent / "models")


def _get_embed_model(name: str):
    if _configured.get("embedding") != name:
        raise HTTPException(status_code=422, detail="请求的嵌入模型未在启动配置中准备")
    return _models[name]


def _get_rerank_model(name: str):
    if _configured.get("rerank") != name:
        raise HTTPException(status_code=422, detail="请求的重排模型未在启动配置中准备")
    return _models[f"rerank:{name}"]


def prepare_models(embed_model: str, rerank_model: str, model_dir: str):
    """完成本地加载后才允许健康返回成功；不会访问模型下载接口。"""
    global _ready, _model_dir
    _ready = False
    _model_dir = model_dir
    if embed_model:
        from sentence_transformers import SentenceTransformer
        _models[embed_model] = SentenceTransformer(
            embed_model, cache_folder=model_dir, local_files_only=True
        )
        _configured["embedding"] = embed_model
    if rerank_model:
        from sentence_transformers import CrossEncoder
        _models[f"rerank:{rerank_model}"] = CrossEncoder(
            rerank_model, cache_folder=model_dir, local_files_only=True
        )
        _configured["rerank"] = rerank_model
    _ready = True


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
    return {"data": [{"index": i, "embedding": e} for i, e in enumerate(embeddings)]}


@app.post("/rerank")
def rerank(req: RerankRequest):
    model = _get_rerank_model(req.model)
    scores = model.predict([(req.query, doc) for doc in req.documents]).tolist()
    results = [{"index": i, "relevance_score": float(s)} for i, s in enumerate(scores)]
    results.sort(key=lambda x: x["relevance_score"], reverse=True)
    if req.top_n:
        results = results[:req.top_n]
    return {"results": results}


@app.get("/health")
def health():
    if not _ready:
        raise HTTPException(status_code=503, detail="本地模型尚未准备完成")
    return {"service": "local-rag-sidecar", "schema_version": "1.0", "status": "ok",
            "instance_id": _instance, "models": dict(_configured)}


def watch_parent(parent_pid: int):
    """父进程异常退出时清理本专属进程组，避免遗留模型进程。"""
    if not parent_pid or os.name == "nt":
        return
    while True:
        if os.getppid() != parent_pid:
            if os.getpgrp() == os.getpid():
                os.killpg(os.getpgrp(), signal.SIGKILL)
            os._exit(1)
        time.sleep(0.2)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="运行已准备好的本地模型，不下载模型")
    parser.add_argument("--port", type=int, default=8766)
    parser.add_argument("--parent-pid", type=int, default=0)
    parser.add_argument("--embed-model", default="")
    parser.add_argument("--rerank-model", default="")
    parser.add_argument("--model-dir", default=_model_dir)
    args = parser.parse_args()
    threading.Thread(target=watch_parent, args=(args.parent_pid,), daemon=True).start()
    try:
        prepare_models(args.embed_model, args.rerank_model, args.model_dir)
    except Exception:
        print("本地模型加载失败。请先准备配置的模型及 Python 依赖；普通启动不会下载模型。", file=sys.stderr)
        sys.exit(1)
    import uvicorn
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=args.port, log_level="warning"))
    if os.name == "nt" and hasattr(signal, "SIGBREAK"):
        # Windows 控制台组用 CTRL_BREAK 请求优雅退出。
        signal.signal(signal.SIGBREAK, lambda *_: setattr(server, "should_exit", True))
    server.run()

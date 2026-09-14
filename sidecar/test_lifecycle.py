"""真实 Python 进程验收；使用已安装依赖，不安装软件或下载模型。"""
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parent


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def read_json(url, payload=None, timeout=2):
    data = None if payload is None else json.dumps(payload).encode()
    request = Request(url, data=data, headers={"Content-Type": "application/json"})
    with urlopen(request, timeout=timeout) as response:
        return json.load(response)


def files_digest(path):
    return {str(file.relative_to(path)): hashlib.sha256(file.read_bytes()).hexdigest()
            for file in Path(path).rglob("*") if file.is_file()}


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="sidecar lifecycle ")
        self.addCleanup(self.directory.cleanup)
        self.log = open(Path(self.directory.name) / "sidecar.log", "wb")
        self.addCleanup(self.log.close)

    def start(self, *args):
        port = free_port()
        env = {**os.environ, "LOCAL_RAG_SIDECAR_TOKEN": "acceptance-instance", "HF_HUB_OFFLINE": "0"}
        child = subprocess.Popen([sys.executable, str(ROOT / "main.py"), "--port", str(port), *args],
                                 stdout=self.log, stderr=self.log, env=env,
                                 start_new_session=os.name != "nt")
        self.addCleanup(self.stop, child)
        return child, f"http://127.0.0.1:{port}"

    def stop(self, child):
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)

    def ready(self, child, base):
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            self.assertIsNone(child.poll(), "sidecar 在健康就绪前退出")
            try:
                return read_json(base + "/health")
            except (OSError, URLError):
                time.sleep(0.1)
        self.fail("sidecar 健康超时")

    def test_identity_and_unconfigured_model(self):
        child, base = self.start()
        health = self.ready(child, base)
        self.assertEqual(health, {"service": "local-rag-sidecar", "schema_version": "1.0",
                                 "status": "ok", "instance_id": "acceptance-instance", "models": {}})
        with self.assertRaises(HTTPError) as result:
            read_json(base + "/embed", {"model": "unconfigured", "input": ["不会发送到模型"]})
        self.assertEqual(result.exception.code, 422)

    def test_missing_model_fails_without_download(self):
        cache = Path(self.directory.name) / "empty cache"
        cache.mkdir()
        before = files_digest(cache)
        child, _ = self.start("--embed-model", "missing-local-rag-acceptance/model-never-download",
                              "--model-dir", str(cache))
        self.assertNotEqual(child.wait(timeout=45), 0)
        self.assertEqual(before, files_digest(cache))
        self.log.flush()
        self.assertIn("普通启动不会下载模型", (Path(self.directory.name) / "sidecar.log").read_text())

    @unittest.skipUnless(os.environ.get("LOCAL_RAG_TEST_EMBED_MODEL"), "未指定已缓存的真实模型")
    def test_cached_model_loads_and_embeds_offline(self):
        model = os.environ["LOCAL_RAG_TEST_EMBED_MODEL"]
        cache = Path(os.environ.get("LOCAL_RAG_TEST_MODEL_DIR", str(ROOT / "models")))
        before = files_digest(cache)
        child, base = self.start("--embed-model", model, "--model-dir", str(cache))
        health = self.ready(child, base)
        self.assertEqual(health["models"], {"embedding": model})
        vectors = read_json(base + "/embed", {"model": model, "input": ["本地服务生命周期验收。"]}, timeout=30)
        self.assertGreater(len(vectors["data"][0]["embedding"]), 0)
        self.stop(child)
        self.assertEqual(before, files_digest(cache))

    @unittest.skipIf(os.name == "nt", "Windows 后代清理由服务 Job Object 验收")
    def test_parent_death_cleans_sidecar_group(self):
        port = free_port()
        parent_code = """import os, subprocess, sys, time
child = subprocess.Popen([sys.executable, sys.argv[1], '--port', sys.argv[2], '--parent-pid', str(os.getpid())], start_new_session=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
print(child.pid, flush=True)
time.sleep(60)
"""
        parent = subprocess.Popen([sys.executable, "-c", parent_code, str(ROOT / "main.py"), str(port)], stdout=subprocess.PIPE, text=True)
        self.addCleanup(self.stop, parent)
        self.addCleanup(parent.stdout.close)
        sidecar_pid = int(parent.stdout.readline())
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                read_json(f"http://127.0.0.1:{port}/health")
                break
            except (OSError, URLError):
                time.sleep(0.1)
        else:
            self.fail("watchdog 验收的 sidecar 未就绪")
        parent.kill()
        parent.wait(timeout=5)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            try:
                os.kill(sidecar_pid, 0)
            except ProcessLookupError:
                return
            time.sleep(0.1)
        self.fail("父进程退出后 sidecar 仍存活")


if __name__ == "__main__":
    unittest.main(verbosity=2)

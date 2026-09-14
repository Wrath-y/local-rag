#!/usr/bin/env python3
"""真实本地模型验收：只使用已准备的缓存，所有数据写入独立临时目录。"""
import argparse
import concurrent.futures
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
SCRIPT_SHA256 = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()


if os.name == "nt":
    import ctypes
    from ctypes import wintypes

    # 显式声明 64 位句柄签名，存活检查绝不调用 Windows 的 os.kill。
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel32.CloseHandle.restype = wintypes.BOOL
    kernel32.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
    kernel32.WaitForSingleObject.restype = wintypes.DWORD
    kernel32.GetProcessTimes.argtypes = [wintypes.HANDLE] + [ctypes.POINTER(wintypes.FILETIME)] * 4
    kernel32.GetProcessTimes.restype = wintypes.BOOL
    kernel32.OpenEventW.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.LPCWSTR]
    kernel32.OpenEventW.restype = wintypes.HANDLE
    kernel32.SetEvent.argtypes = [wintypes.HANDLE]
    kernel32.SetEvent.restype = wintypes.BOOL

    class ProcessEntry(ctypes.Structure):
        _fields_ = [("dwSize", wintypes.DWORD), ("cntUsage", wintypes.DWORD),
                    ("th32ProcessID", wintypes.DWORD), ("th32DefaultHeapID", ctypes.c_size_t),
                    ("th32ModuleID", wintypes.DWORD), ("cntThreads", wintypes.DWORD),
                    ("th32ParentProcessID", wintypes.DWORD), ("pcPriClassBase", wintypes.LONG),
                    ("dwFlags", wintypes.DWORD), ("szExeFile", wintypes.WCHAR * 260)]

    kernel32.CreateToolhelp32Snapshot.argtypes = [wintypes.DWORD, wintypes.DWORD]
    kernel32.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    for name in ("Process32FirstW", "Process32NextW"):
        function = getattr(kernel32, name)
        function.argtypes = [wintypes.HANDLE, ctypes.POINTER(ProcessEntry)]
        function.restype = wintypes.BOOL


def windows_process(pid):
    handle = kernel32.OpenProcess(0x1000 | 0x100000, False, pid)
    if not handle:
        error = ctypes.get_last_error()
        if error == 87:  # PID 已不存在。
            return None
        raise ctypes.WinError(error)
    return handle


def windows_creation_time(pid):
    handle = windows_process(pid)
    if handle is None:
        return None
    try:
        if kernel32.WaitForSingleObject(handle, 0) == 0:
            return None
        times = [wintypes.FILETIME() for _ in range(4)]
        if not kernel32.GetProcessTimes(handle, *(ctypes.byref(value) for value in times)):
            raise ctypes.WinError(ctypes.get_last_error())
        return times[0].dwHighDateTime << 32 | times[0].dwLowDateTime
    finally:
        kernel32.CloseHandle(handle)


def request_shutdown(proc):
    if proc.poll() is not None:
        return
    if os.name != "nt":
        proc.send_signal(signal.SIGTERM)
        return
    creation = windows_creation_time(proc.pid)
    if creation is None:
        return
    # 与 Go ShutdownContext 的命名事件协议一致，支持无控制台的编排器。
    name = f"Local\\local-rag-stop-{proc.pid}-{creation >> 32}-{creation & 0xffffffff}"
    event = kernel32.OpenEventW(0x0002, False, name)
    if not event:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        if not kernel32.SetEvent(event):
            raise ctypes.WinError(ctypes.get_last_error())
    finally:
        kernel32.CloseHandle(event)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def alive(pid):
    if os.name == "nt":
        handle = windows_process(pid)
        if handle is None:
            return False
        try:
            state = kernel32.WaitForSingleObject(handle, 0)
            if state == 0xffffffff:
                raise ctypes.WinError(ctypes.get_last_error())
            return state == 258
        finally:
            kernel32.CloseHandle(handle)
    try:
        os.kill(pid, 0)
        state = subprocess.run(["ps", "-p", str(pid), "-o", "stat="], capture_output=True, text=True).stdout
        return bool(state.strip()) and not state.strip().startswith("Z")
    except ProcessLookupError:
        return False


def wait_stopped(pids, timeout=12):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not any(alive(pid) for pid in pids):
            return
        time.sleep(.1)
    raise AssertionError(f"子进程未回收：{[pid for pid in pids if alive(pid)]}")


def child_pids(pid):
    # 快照整棵后代树，不能把 Windows 的空列表当作子进程回收证据。
    if os.name == "nt":
        snapshot = kernel32.CreateToolhelp32Snapshot(0x00000002, 0)
        if snapshot == ctypes.c_void_p(-1).value:
            raise ctypes.WinError(ctypes.get_last_error())
        parents = {}
        try:
            entry = ProcessEntry()
            entry.dwSize = ctypes.sizeof(entry)
            found = kernel32.Process32FirstW(snapshot, ctypes.byref(entry))
            while found:
                parents[entry.th32ProcessID] = entry.th32ParentProcessID
                found = kernel32.Process32NextW(snapshot, ctypes.byref(entry))
            if ctypes.get_last_error() != 18:
                raise ctypes.WinError(ctypes.get_last_error())
        finally:
            kernel32.CloseHandle(snapshot)
        creation = {pid: windows_creation_time(pid)}
    else:
        result = subprocess.run(["ps", "-axo", "pid=,ppid="], text=True, capture_output=True, check=True)
        parents = {int(fields[0]): int(fields[1]) for line in result.stdout.splitlines()
                   if len(fields := line.split()) == 2}
    owned = {pid}
    pending = [pid]
    while pending:
        parent = pending.pop()
        for child, ppid in parents.items():
            if ppid != parent or child in owned:
                continue
            if os.name == "nt":
                try:
                    started = windows_creation_time(child)
                except OSError:
                    continue
                if started is None or creation[parent] is None or started < creation[parent]:
                    continue
                creation[child] = started
            owned.add(child)
            pending.append(child)
    return sorted(owned - {pid})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", default="BAAI/bge-small-zh-v1.5")
    platform_id = platform.system().lower()
    parser.add_argument("--output", default=str(ROOT / f".run/lifecycle-acceptance-{platform_id}.json"))
    args = parser.parse_args()
    binary = ROOT / ".run/bin" / ("rag-server.exe" if os.name == "nt" else "rag-server")
    binary.parent.mkdir(parents=True, exist_ok=True)
    run_dir = Path(tempfile.mkdtemp(prefix="local rag acceptance "))
    port, sidecar_port = free_port(), free_port()
    config = run_dir / "config with spaces.yaml"
    config.write_text(f"embedding:\n  provider: local\n  model: {json.dumps(args.model)}\n  dims: 512\nrerank:\n  provider: disabled\nsidecar:\n  port: {sidecar_port}\n  startup_timeout: 60\nstorage:\n  db_path: {json.dumps(str(run_dir / 'data' / 'rag.db'))}\n", encoding="utf-8")
    common = ["--config", str(config), "--workdir", str(ROOT), "--listen", f"127.0.0.1:{port}", "--pid-dir", str(run_dir / "run"), "--log-dir", str(run_dir / "logs"), "--health-timeout", "90s"]
    results = []
    pids = []
    proc = foreground = None
    log = None
    failure = None
    cleanup = []

    def command(action, extra=None, expect=0, base=None):
        result = subprocess.run([str(binary), "lifecycle", action] + (common if base is None else base) + (extra or []), cwd=tempfile.gettempdir(), text=True, encoding="utf-8", capture_output=True, timeout=120)
        if expect == 0 and result.returncode != 0:
            raise AssertionError(f"{action} 失败：{result.stderr}；{result.stdout}")
        if expect != 0 and result.returncode == 0:
            raise AssertionError(f"{action} 应当非零退出")
        return json.loads(result.stdout) if result.returncode == 0 else result

    def health():
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/health", timeout=5) as response:
            return json.load(response)

    try:
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/server"], cwd=ROOT, check=True)
        started = command("start", ["--background"])
        pids = [started["pid"]] + child_pids(started["pid"])
        assert len(pids) >= 2, "未观察到真实 sidecar 子进程"
        assert started["ownership"] == "managed" and started["state"] == "running"
        document = health()
        assert document["service"] == "local-rag" and document["schema_version"] == "1.0"
        results.append("真实本地模型、服务健康、独立 DB、任意工作目录及空格参数路径")
        repeated = command("start", ["--background"])
        assert repeated["pid"] == started["pid"] and not repeated["started_this_run"]
        assert command("status")["pid"] == started["pid"]
        results.append("重复启动和 JSON 状态保留同一实例")
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            attempts = list(pool.map(lambda _: subprocess.run([str(binary), "lifecycle", "start"] + common + ["--background"], capture_output=True, text=True, encoding="utf-8", timeout=15), range(2)))
        assert any(result.returncode == 0 for result in attempts)
        assert all(result.returncode != 0 or json.loads(result.stdout)["pid"] == started["pid"] for result in attempts)
        results.append("并发启动由锁串行化或拒绝，未创建重复实例")
        external = common + ["--pid-dir", str(run_dir / "external")]
        assert command("start", ["--background"], base=external)["ownership"] == "external"
        assert command("stop", base=external)["ownership"] == "external"
        assert health()["service"] == "local-rag"
        results.append("外部真实 local-rag 复用及停止保留")
        stale = run_dir / "stale"
        stale.mkdir()
        bad = dict(started)
        bad["start_time"] += "-stale"
        (stale / "instance.json").write_text(json.dumps(bad))
        command("stop", expect=1, base=["--pid-dir", str(stale)])
        assert alive(started["pid"])
        results.append("陈旧 PID 身份记录拒绝终止仍运行的服务")
        with socket.socket() as occupied:
            occupied.bind(("127.0.0.1", 0)); occupied.listen()
            occupied_port = occupied.getsockname()[1]
            command("start", ["--background", "--listen", f"127.0.0.1:{occupied_port}", "--pid-dir", str(run_dir / "occupied")], expect=1)
        results.append("无关监听端口拒绝启动")
        command("stop")
        wait_stopped(pids)
        assert command("stop")["state"] == "stopped"
        assert Path(started["log_path"]).is_file() and (run_dir / "data" / "rag.db").is_file()
        results.append("优雅停止回收真实 sidecar、重复停止幂等且保留数据日志")
        pids = []
        failed = command("start", ["--background", "--health-timeout", "1ms"], expect=1)
        failed_state = json.loads((run_dir / "run" / "instance.json").read_text())
        wait_stopped([failed_state["pid"]])
        results.append("健康超时返回非零并回收本轮进程")
        command("stop")
        invalid_config = run_dir / "unusable storage.yaml"
        invalid_config.write_text(config.read_text().replace(json.dumps(str(run_dir / "data" / "rag.db")), json.dumps(str(run_dir))))
        command("start", ["--background", "--config", str(invalid_config)], expect=1)
        failed_state = json.loads((run_dir / "run" / "instance.json").read_text())
        wait_stopped([failed_state["pid"]])
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{sidecar_port}/health", timeout=1)
            raise AssertionError("存储初始化失败后 sidecar 仍存活")
        except OSError:
            pass
        command("stop")
        results.append("真实 sidecar 成功后存储初始化失败返回非零且回收 sidecar")
        foreground = subprocess.Popen([str(binary), "lifecycle", "start"] + common, cwd=tempfile.gettempdir(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8")
        foreground_state = json.loads(foreground.stdout.readline())
        assert foreground.poll() is None and foreground_state["state"] == "running"
        pids = [foreground_state["pid"]] + child_pids(foreground_state["pid"])
        command("stop")
        assert foreground.wait(timeout=25) == 0
        wait_stopped(pids)
        pids = []
        results.append("独立控制器默认前台驻留且停止后返回零")
        log = open(run_dir / "orchestrated.log", "ab")
        proc = subprocess.Popen([str(binary), "serve", "--config", str(config), "--listen", f"127.0.0.1:{port}"], cwd=ROOT, stdout=log, stderr=log)
        pids = [proc.pid]
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise AssertionError("被编排服务提前退出")
            try:
                if health()["service"] == "local-rag":
                    break
            except Exception:
                pass
            time.sleep(.2)
        else:
            raise AssertionError("被编排服务健康超时")
        pids += child_pids(proc.pid)
        assert len(pids) >= 2, "被编排实例未观察到 sidecar"
        request_shutdown(proc)
        assert proc.wait(timeout=25) == 0
        wait_stopped(pids)
        log.close()
        pids = []
        shutdown_method = "Windows 命名事件" if os.name == "nt" else "SIGTERM"
        results.append(f"真实 serve 前台被编排启动、{shutdown_method} 返回零及父子进程回收")
        log = open(run_dir / "parent-death.log", "ab")
        proc = subprocess.Popen([str(binary), "serve", "--config", str(config), "--listen", f"127.0.0.1:{port}"], cwd=ROOT, stdout=log, stderr=log)
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise AssertionError("父死清理验收的服务提前退出")
            try:
                if health()["service"] == "local-rag":
                    break
            except OSError:
                pass
            time.sleep(.2)
        else:
            raise AssertionError("父死清理验收的服务健康超时")
        pids = [proc.pid] + child_pids(proc.pid)
        assert len(pids) >= 2, "父死清理验收未观察到 sidecar"
        # 只强制终止本轮 Popen 句柄，验证 Job Object 或 watchdog 回收真实后代。
        proc.kill()
        proc.wait(timeout=10)
        wait_stopped(pids)
        pids = []
        log.close()
        cleanup_method = "Job Object" if os.name == "nt" else "watchdog"
        results.append(f"受管服务异常退出后由 {cleanup_method} 回收真实 sidecar 后代")
        if os.name != "nt":
            linked_root = run_dir / "repository with spaces"
            linked_root.symlink_to(ROOT, target_is_directory=True)
            scripted = subprocess.run([str(linked_root / "start.sh"), "--background"] + common, cwd=tempfile.gettempdir(), capture_output=True, text=True, timeout=120)
            assert scripted.returncode == 0, scripted.stderr
            scripted_state = json.loads(scripted.stdout)
            pids = [scripted_state["pid"]] + child_pids(scripted_state["pid"])
            stopped = subprocess.run([str(linked_root / "stop.sh"), "--pid-dir", str(run_dir / "run")], cwd=tempfile.gettempdir(), capture_output=True, text=True, timeout=40)
            assert stopped.returncode == 0, stopped.stderr
            wait_stopped(pids)
            pids = []
            missing_tools = run_dir / "tools without go"
            missing_tools.mkdir()
            # Linux 的 /usr/bin 可能已有 Go；仅提供执行缺工具检查所需的 dirname。
            (missing_tools / "dirname").symlink_to(shutil.which("dirname"))
            no_tools = subprocess.run([str(linked_root / "start.sh")], cwd=tempfile.gettempdir(), env={"PATH": str(missing_tools)}, capture_output=True, text=True, encoding="utf-8", timeout=10)
            assert no_tools.returncode != 0 and "缺少 Go" in no_tools.stderr
            results.append("真实 Unix 脚本经空格仓库路径和任意目录启动停止、缺工具中文非零")
        else:
            # 使用物理空格目录和已构建程序，不依赖 Windows 符号链接权限。
            scripted_root = run_dir / "repository with spaces"
            (scripted_root / ".run/bin").mkdir(parents=True)
            shutil.copy2(binary, scripted_root / ".run/bin/rag-server.exe")
            for entry in ("start.bat", "stop.bat", "status.bat"):
                shutil.copy2(ROOT / entry, scripted_root / entry)

            def batch(name, arguments):
                # 批处理经 cmd 解析；参数仅来自本验收生成的路径/端口。
                return subprocess.run([str(scripted_root / name)] + arguments,
                                      cwd=tempfile.gettempdir(), capture_output=True,
                                      text=True, encoding="utf-8", timeout=120)

            scripted = batch("start.bat", ["--background"] + common)
            assert scripted.returncode == 0, scripted.stderr
            scripted_state = json.loads(scripted.stdout)
            pids = [scripted_state["pid"]] + child_pids(scripted_state["pid"])
            assert len(pids) >= 2, "批处理入口未观察到 sidecar"
            status = batch("status.bat", ["--pid-dir", str(run_dir / "run")])
            assert status.returncode == 0 and json.loads(status.stdout)["pid"] == scripted_state["pid"]
            stopped = batch("stop.bat", ["--pid-dir", str(run_dir / "run")])
            assert stopped.returncode == 0, stopped.stderr
            wait_stopped(pids)
            pids = []
            results.append("真实 Windows 批处理经物理空格目录和任意目录启动、查询、停止并回收后代")
    except BaseException as error:
        failure = f"{type(error).__name__}: {error}"
        raise
    finally:
        # 只通过自己创建的台账或 Popen 句柄收尾，绝不按名称或端口清理。
        def cleanup_step(name, action):
            try:
                details = action() or {}
                cleanup.append({"name": name, "status": "passed", **details})
            except BaseException as error:
                cleanup.append({"name": name, "status": "failed", "error": f"{type(error).__name__}: {error}"})

        def stop_ledger():
            if not binary.is_file():
                return {"status": "skipped", "reason": "生命周期程序尚未构建，未启动服务"}
            result = subprocess.run([str(binary), "lifecycle", "stop"] + common,
                                    capture_output=True, text=True, encoding="utf-8", timeout=40)
            return {"status": "passed" if result.returncode == 0 else "failed",
                    "returncode": result.returncode, "stdout": result.stdout, "stderr": result.stderr}

        def stop_direct():
            if proc is None or proc.poll() is not None:
                return {"status": "skipped", "reason": "没有仍运行的直接子进程"}
            try:
                request_shutdown(proc)
                proc.wait(timeout=25)
                return {"method": "graceful", "returncode": proc.returncode}
            except (OSError, subprocess.TimeoutExpired) as error:
                proc.kill()
                proc.wait(timeout=10)
                return {"method": "Popen.kill", "returncode": proc.returncode,
                        "graceful_error": f"{type(error).__name__}: {error}"}

        def close_foreground():
            if foreground is None:
                return {"status": "skipped", "reason": "没有前台控制器"}
            if foreground.poll() is None:
                foreground.terminate()
                foreground.wait(timeout=10)
            foreground.stdout.close()
            foreground.stderr.close()
            return {"returncode": foreground.returncode}

        cleanup_step("lifecycle stop", stop_ledger)
        cleanup_step("直接子进程", stop_direct)
        cleanup_step("前台控制器", close_foreground)
        cleanup_step("后代退出核验", lambda: wait_stopped(pids))
        if log is not None:
            cleanup_step("日志句柄", log.close)

        def version(arguments):
            try:
                return subprocess.check_output(arguments, text=True, encoding="utf-8", stderr=subprocess.STDOUT, timeout=10).strip()
            except (OSError, subprocess.SubprocessError) as error:
                return f"无法读取：{error}"

        sidecar_python = ROOT / "sidecar/.venv" / ("Scripts/python.exe" if os.name == "nt" else "bin/python3")
        if not sidecar_python.is_file():
            sidecar_python = Path(shutil.which("python" if os.name == "nt" else "python3") or "")
        status = "passed" if failure is None and all(step["status"] != "failed" for step in cleanup) else "failed"
        evidence = {"date": datetime.datetime.now(datetime.timezone.utc).isoformat(), "status": status,
                    "failure": failure, "cleanup": cleanup, "platform": platform.platform(),
                    "platform_id": platform_id, "machine": platform.machine(),
                    "go_version": version(["go", "version"]),
                    "python_version": sys.version, "sidecar_python": str(sidecar_python),
                    "sidecar_python_version": version([str(sidecar_python), "--version"]),
                    "script_sha256": SCRIPT_SHA256,
                    "model": args.model, "run_dir": str(run_dir), "checks": results,
                    "limitations": ["本记录仅证明当前平台实际运行；其他平台需分别运行，交叉编译不算运行验收"]}
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        staged = output.with_suffix(output.suffix + ".tmp")
        staged.write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        staged.replace(output)
        print(json.dumps(evidence, ensure_ascii=False, indent=2))
        if failure is None and status != "passed":
            raise AssertionError("验收清理失败，请查看机器记录中的 cleanup")


if __name__ == "__main__":
    main()

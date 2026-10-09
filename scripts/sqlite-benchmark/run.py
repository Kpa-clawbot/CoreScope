#!/usr/bin/env python3
"""Isolated, serial, source-pinned CoreScope application comparison.

Both archived revisions use native CGO SQLite. A successful invocation means
validation completed; it does not establish a general speed or capacity claim.
"""
import argparse
import csv
import decimal
import datetime
import errno
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tarfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

UPSTREAM = "9dbc287579a237ffa744dd0c91fa7227d09763ac"
FIRMWARE = "a366955cb2f67b8e6842d4f00d2b6a554dddd88a"
HERE = Path(__file__).resolve().parent
PROCESSES = []
RESOURCE_COUNTERS = ("cgroup.procs", "memory.current", "memory.events", "cpu.stat", "io.stat")
# Common to both engines: B itself accounts for about 1,222 MiB before replay.
SERVER_MEMORY_MIB = 3072
PACKET_STORE_MIB = 2048
TABLES = ["nodes", "inactive_nodes", "observers", "transmissions", "observations", "observer_metrics", "neighbor_edges", "dropped_packets", "client_receptions", "client_observers", "client_rx_observations", "client_rf_samples", "node_declared_regions", "scope_match_totals", "_migrations", "_async_migrations", "advert_route_evidence", "advert_evidence_backfill"]
# These are operational receipt/creation times, not packet timestamps. Full
# pre-run parity includes them; only post-replay logical parity excludes them.
VOLATILE = {"transmissions": {"created_at"}, "observers": {"last_seen", "last_packet_at", "first_seen"}, "_async_migrations": {"started_at", "ended_at"}, "scope_match_totals": {"updated_unix", "since_unix"}}
TEXT_TIMES = {"first_seen", "last_seen", "created_at", "last_packet_at", "clock_last_naive_at", "configured_scope_at", "timestamp", "dropped_at", "rx_at", "ingested_at", "sampled_at", "observed_at", "started_at", "ended_at"}


def shift_timestamp(value, delta):
    if value is None or value == "":
        return value
    if not re.fullmatch(r"\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)?", value):
        raise ValueError("unsupported synthetic timestamp")
    head = datetime.datetime.strptime(value[:19].replace("T", " "), "%Y-%m-%d %H:%M:%S") + datetime.timedelta(seconds=delta)
    return head.strftime("%Y-%m-%d" + value[10] + "%H:%M:%S") + value[19:]


def freshen(path, delta):
    """One recorded receipt-time offset; signed wire bytes remain unchanged."""
    db = sqlite3.connect(path)
    try:
        db.create_function("bench_shift", 1, lambda value: shift_timestamp(value, delta))
        for table in TABLES:
            columns = list(db.execute(f'PRAGMA table_info("{table}")'))
            updates = []
            for _, name, kind, *_ in columns:
                if name not in TEXT_TIMES and not (table == "scope_match_totals" and name in ("since_unix", "updated_unix")):
                    continue
                if kind.upper() in ("INTEGER", "INT", "BIGINT"):
                    updates.append(f'"{name}"="{name}"+{int(delta)}')
                elif kind.upper() in ("TEXT", "DATETIME"):
                    updates.append(f'"{name}"=bench_shift("{name}")')
            if updates:
                db.execute(f'UPDATE "{table}" SET ' + ",".join(updates))
        db.commit()
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    finally:
        db.close()


def corpus(name):
    return {"S": dict(transmissions=30_000, observations=90_000, nodes=2000, observers=32, days=8), "B": dict(transmissions=128_000, observations=2_048_000, nodes=2000, observers=128, days=8), "L": dict(transmissions=1_125_000, observations=18_000_000, nodes=5000, observers=512, days=30)}[name].copy()


def fanout(name, index):
    return index % 5 + 1 if name == "S" else (4 if index % 10 < 5 else 22 if index % 10 < 7 else 23 if index % 10 < 9 else 50)


def pair_order(pairs):
    return [(i, variant) for i in range(pairs) for variant in (("baseline", "candidate") if i % 2 == 0 else ("candidate", "baseline"))]


def percentiles(values):
    values = sorted(values)
    def q(p):
        return values[max(0, math.ceil(p * len(values)) - 1)] if values else None
    return dict(samples=len(values), p50=q(.50), p95=q(.95), p99=q(.99) if len(values) >= 1000 else None, maximum=q(1))


def visibility(ingest, messages):
    sent = {e["hash"]: e for e in ingest if e.get("new_transmission") and not e.get("error") and not e.get("dropped")}
    measured = {key: event for key, event in sent.items() if event.get("measured")}
    received, duplicate_receipts, duplicate_receipts_all = {}, 0, 0
    for message in messages:
        key = message.get("hash")
        if key not in sent:
            continue
        if key in received:
            duplicate_receipts_all += 1
            duplicate_receipts += int(key in measured)
        received[key] = min(received.get(key, message["received_ns"]), message["received_ns"])
    missing = sorted(set(sent) - set(received))
    lags = [received[key] - event["completed_ns"] for key, event in measured.items() if key in received]
    return dict(verified=not missing, verification_scope="full stream including warmup", sample_scope="measured events only",
                sentinels=len(sent), received=len(received), missing=len(missing), missing_hashes=missing[:100],
                measured_sentinels=len(measured), measured_received=len(measured.keys() & received.keys()), measured_missing=len(measured.keys() - received.keys()),
                duplicate_hash_receipts=duplicate_receipts, duplicate_hash_receipts_all=duplicate_receipts_all,
                visible_before_ingest_return=sum(x < 0 for x in lags),
                visible_before_ingest_return_all=sum(received[key] < event["completed_ns"] for key, event in sent.items() if key in received),
                lag_ns=percentiles(lags), samples_ns=lags)


def json_lines(path):
    with open(path) as stream:
        for line in stream:
            yield json.loads(line)


def canonical_row(row):
    out = []
    for value in row:
        if value is None:
            out.append(["null"])
        elif isinstance(value, (int, float, decimal.Decimal)) and not isinstance(value, bool):
            number = decimal.Decimal(str(value))
            if not number.is_finite():
                raise ValueError("non-finite number in corpus")
            out.append(["number", str(number.normalize()) if number else "0"])
        elif isinstance(value, str):
            out.append(["text", value])
        else:
            raise ValueError("unsupported corpus cell type")
    return (json.dumps(out, ensure_ascii=False, separators=(",", ":")) + "\n").encode()


def safe_output(path):
    path = Path(path).absolute()
    if not path.name.startswith("corescope-bench-") or path.exists() or path.is_symlink():
        raise ValueError("output must be a new directory named corescope-bench-<id>")
    if not path.parent.is_dir():
        raise ValueError("output parent must already exist")
    return path.resolve()


def check_revisions(baseline, candidate):
    if not all(re.fullmatch(r"[0-9a-f]{40}", ref) for ref in (baseline, candidate)):
        raise ValueError("baseline and candidate must be complete lowercase 40-character commit SHAs")


def validate_profile(profile, pairs, cgroup):
    if profile == "primary" and (pairs != 5 or not cgroup):
        raise ValueError("primary requires five paired runs and a delegated CPU/memory cgroup")
    if profile == "diagnostic" and (pairs != 1 or not cgroup):
        raise ValueError("diagnostic requires one pair and the same delegated CPU/memory cgroup")
    if not 1 <= pairs <= 10:
        raise ValueError("pairs must be between one and ten")


def write_json(path, value):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(str(path) + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def safe_diagnostics(text):
    """Keep diagnostic lines only; never publish arbitrary child log tails."""
    out = []
    for line in text.splitlines():
        line = line.strip()
        if not re.match(r"(?:--- FAIL:|FAIL\b|panic:|fatal error:|[^\s]*\.go:\d+(?::\d+)?:|ERROR:|FATAL:|error:|benchmark:)", line):
            codes = re.findall(r"SQLSTATE [0-9A-Z]{5}", line)
            if codes:
                out.extend(codes)
            continue
        if line.startswith("panic:") and not line.startswith("panic: runtime error:"):
            line = "panic: <value redacted>"
        elif re.search(r"\b(?:host|hostaddr|user|dbname|password|sslmode)\s*=", line, re.I):
            line = "database connection error: <connection details redacted>"
        else:
            line = re.sub(r"[a-zA-Z][a-zA-Z0-9+.-]*://\S+", "<url redacted>", line)
            line = re.sub(r"\b[A-Za-z]:[\\/]\S+|(?<![A-Za-z0-9_])/(?:[^\s'\"]+/)*[^\s'\"]+", "<path redacted>", line)
            line = re.sub(r"\{.*\}|\[.*\]", "<structured data redacted>", line)
            line = re.sub(r'"[^"\n]*"|\x27[^\x27\n]*\x27', "<quoted value redacted>", line)
            line = re.sub(r"\b(?:raw|payload|packet|message|body|authorization|cookie|password|passwd|secret|token|dsn|database_url|credentials?)\s*[:=].*", "<data redacted>", line, flags=re.I)
            line = re.sub(r"\b[0-9a-fA-F]{16,}\b|(?<!\w)[A-Za-z0-9+/]{32,}={0,2}", "<encoded data redacted>", line)
        out.append(line[:240])
    return out[-10:]





def record_failure(log, exit_code):
    log = Path(log)
    with open(log, "rb") as stream:
        stream.seek(max(0, log.stat().st_size - 8192))
        tail = stream.read(8192).decode("utf-8", errors="replace")
    result = dict(log=log.name, exit_code=exit_code, diagnostics=safe_diagnostics(tail), policy="At most 10 sanitized diagnostic lines from the final 8 KiB; raw logs and event bodies excluded")
    write_json(log.with_name("failure-" + log.stem + ".json"), result)
    return result


def record_resource_failure(path, error, **context):
    filename = getattr(error, "filename", None)
    name = Path(filename).name if filename else None
    known = (*RESOURCE_COUNTERS, "memory.peak", "smaps_rollup", "comm", "resources.csv", "processes.csv", "resource-final.json", "resource-error.json")
    detail = dict(error_type=type(error).__name__, errno=getattr(error, "errno", None),
                  counter=name if name in known else None, **context)
    try:
        write_json(path, detail)
    except OSError as write_error:
        # A full/unwritable output filesystem must not replace the original
        # workload exception with another cleanup exception.
        detail["evidence_write_error_type"] = type(write_error).__name__
        detail["evidence_write_errno"] = write_error.errno
    return detail


def public_bundle(output, repository=None):
    """Explicit whitelist: never export databases, event bodies or controls."""
    output = Path(output).resolve()
    public = output / "public"
    public.mkdir(exist_ok=True)
    patterns = ["manifest.json", "resource-preflight.json", "summary*.csv", "paired-summary.json", "report.md", "logs/failure-*.json", "corpus/*.json", "pair-*/corpus-offset.json", "pair-*/events.json", "pair-*/*/failure-*.json", "pair-*/*/*.jsonl", "pair-*/*/resources.csv", "pair-*/*/processes.csv", "pair-*/*/resource-final.json", "pair-*/*/resource-error.json", "pair-*/*/*validation*.json", "pair-*/*/startup*.json", "pair-*/*/retention*.json", "pair-*/*/handler.json", "pair-*/*/database-settings.json", "pair-*/*/http-workload.json", "pair-*/*/visibility.json", "pair-*/*/plans/*", "pair-*/*/db-pool.json", "pair-*/*/profiles/*-top.txt", "pair-*/*/profile-capture.json"]
    for pattern in patterns:
        for source in sorted(output.glob(pattern)):
            if not source.is_file() or source.is_symlink():
                continue
            destination = public / source.relative_to(output)
            destination.parent.mkdir(parents=True, exist_ok=True)
            text = source.read_text()
            text = text.replace(str(output), "<benchmark>")
            if repository:
                text = text.replace(str(Path(repository).resolve()), "<repository>")
            destination.write_text(text)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def remove_working_directory(path, runtime_root):
    path, runtime_root = Path(path).resolve(), Path(runtime_root).resolve()
    if runtime_root.name != ".runtime" or not path.is_relative_to(runtime_root) or not re.fullmatch(r"config-\d+-(baseline|candidate)", path.name):
        raise ValueError("refusing cleanup outside an owned benchmark run")
    shutil.rmtree(path)


def command(args, *, cwd=None, env=None, log=None, input=None, timeout=900, budget=None):
    args = [str(x) for x in args]
    proc = subprocess.Popen(budget.arguments(args) if budget else args, cwd=cwd, env=env, stdin=subprocess.PIPE, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
    PROCESSES.append(proc)
    try:
        result, _ = proc.communicate(input, timeout=timeout)
    except BaseException:
        stop(proc)
        raise
    if log:
        Path(log).write_text(result)
    if proc.returncode:
        if not log:
            diagnostic="; ".join(safe_diagnostics(result[-8192:])) or "no recognized safe diagnostic"
            raise RuntimeError(f"{Path(str(args[0])).name} failed (exit {proc.returncode}): {diagnostic}")
        record_failure(log, proc.returncode)
        raise RuntimeError(f"{Path(str(args[0])).name} failed (exit {proc.returncode}); see failure-{Path(log).stem}.json")
    return result


def production_hash(root):
    h = hashlib.sha256()
    for folder in ("cmd", "internal", "public"):
        for path in sorted((Path(root) / folder).rglob("*")):
            if path.is_file() and not path.name.endswith("_test.go"):
                h.update(path.relative_to(root).as_posix().encode() + b"\0")
                h.update(bytes.fromhex(sha256(path)))
    return h.hexdigest()


def archive(repo, revision, destination):
    actual = command(["git", "rev-parse", revision + "^{commit}"], cwd=repo).strip()
    if actual != revision:
        raise ValueError("revision did not resolve to the exact requested commit")
    destination.mkdir()
    proc = subprocess.Popen(["git", "archive", "--format=tar", revision], cwd=repo, stdout=subprocess.PIPE)
    try:
        with tarfile.open(fileobj=proc.stdout, mode="r|") as archive_file:
            archive_file.extractall(destination, filter="data")
    finally:
        proc.stdout.close()
        if proc.wait() != 0:
            raise RuntimeError("git archive failed")


class Budget:
    def __init__(self, parent, required):
        self.path = None
        self._memory_peak_file = None
        self.required = required
        self.info = {"enforced": False, "cpu_limit": None, "memory_limit_bytes": None}
        if not parent:
            return
        parent = Path(parent).resolve()
        if not str(parent).startswith("/sys/fs/cgroup/") or parent == Path("/sys/fs/cgroup"):
            raise ValueError("cgroup must be a delegated subtree, not the global cgroup root")
        self.path = parent / ("corescope-bench-" + str(os.getpid()))
        self.path.mkdir()
        try:
            (self.path / "cpu.max").write_text("300000 100000")
            (self.path / "memory.max").write_text(str(6 * 1024 ** 3))
            if (self.path / "memory.swap.max").exists():
                (self.path / "memory.swap.max").write_text("0")
            cpu, memory = 3.0, 6 * 1024 ** 3
            for ancestor in (self.path, *self.path.parents):
                if ancestor == Path("/sys/fs"):
                    break
                if (ancestor / "cpu.max").exists():
                    q, period = (ancestor / "cpu.max").read_text().split()
                    if q != "max":
                        cpu = min(cpu, int(q) / int(period))
                if (ancestor / "memory.max").exists():
                    value = (ancestor / "memory.max").read_text().strip()
                    if value != "max":
                        memory = min(memory, int(value))
            if cpu < 3 or memory < 6 * 1024 ** 3:
                raise ValueError("ancestor cgroup limits are below the shared 3-CPU/6-GiB budget")
            cpuset = self.path / "cpuset.cpus.effective"
            allowed = set(os.sched_getaffinity(0))
            if cpuset.exists():
                effective = set()
                for part in cpuset.read_text().strip().split(","):
                    bounds = [int(x) for x in part.split("-")]
                    effective.update(range(bounds[0], bounds[-1] + 1))
                allowed &= effective
            if len(allowed) < 3:
                raise ValueError("effective CPU affinity provides fewer than three application CPUs")
            self.info = dict(enforced=True, cpu_limit=cpu, memory_limit_bytes=memory, allowed_cpus=sorted(allowed))
        except Exception:
            self.path.rmdir()
            self.path = None
            raise

    def preflight(self):
        if self.path:
            for name in RESOURCE_COUNTERS:
                (self.path / name).read_text()
            self.info["resource_counters"] = list(RESOURCE_COUNTERS)

    def arguments(self, args):
        if not self.path:
            return args
        # No Python preexec_fn: the resource sampler is threaded. Arguments are
        # positional shell parameters, never interpolated into shell source.
        return ["/bin/sh", "-c", 'printf "%s" "$$" > "$1/cgroup.procs" || exit 125; shift; exec "$@"', "corescope-bench", str(self.path), *args]

    def verify(self, pid):
        if self.path:
            for _ in range(100):
                if str(pid) in (self.path / "cgroup.procs").read_text().split():
                    return
                time.sleep(.01)
            raise RuntimeError("application/database process failed to enter the benchmark cgroup")

    def reset(self):
        self.close_memory_peak()
        self.info["memory_peak_reset"] = False
        if not self.path:
            return
        if (self.path / "cgroup.procs").read_text().strip():
            raise RuntimeError("previous variant is still active; serial comparison refused")
        reclaim = self.path / "memory.reclaim"
        if not reclaim.exists():
            raise RuntimeError("primary cgroup needs memory.reclaim to remove inactive prior-variant charges")
        amount = int((self.path / "memory.current").read_text())
        if amount:
            try:
                reclaim.write_text(str(amount))
            except OSError as error:
                if error.errno != errno.EAGAIN:
                    raise
        remaining = int((self.path / "memory.current").read_text())
        if remaining > 64 * 1024 ** 2:
            raise RuntimeError("more than 64 MiB of inactive prior-variant cgroup memory remains")
        self.info["inactive_charge_reset_bytes"] = remaining
        try:
            # Linux resets memory.peak only for subsequent reads through this
            # same open file description, not later opens of the counter.
            self._memory_peak_file = (self.path / "memory.peak").open("r+b", buffering=0)
            self._memory_peak_file.write(b"0")
            self.info["memory_peak_reset"] = True
        except OSError:
            self.close_memory_peak()

    def memory_peak(self):
        if self._memory_peak_file is None:
            return None
        self._memory_peak_file.seek(0)
        return int(self._memory_peak_file.read())

    def close_memory_peak(self):
        if self._memory_peak_file is not None:
            self._memory_peak_file.close()
            self._memory_peak_file = None

    def close(self):
        self.close_memory_peak()
        if self.path:
            if (self.path / "cgroup.procs").read_text().strip():
                raise RuntimeError("owned benchmark processes remain; refusing cgroup removal")
            self.path.rmdir()


def spawn(args, *, cwd, env, log, budget=None):
    stream = open(log, "w")
    args = [str(x) for x in args]
    proc = subprocess.Popen(budget.arguments(args) if budget else args, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
    PROCESSES.append(proc)
    proc.bench_log = stream
    if budget:
        budget.verify(proc.pid)
    return proc


def finish(proc, timeout=900):
    try:
        result = proc.wait(timeout=timeout)
        if result:
            record_failure(proc.bench_log.name, result)
            raise RuntimeError(f"benchmark child exited {result}; see failure-{Path(proc.bench_log.name).stem}.json")
    finally:
        if proc.poll() is None:
            os.killpg(proc.pid, signal.SIGTERM)
            try:
                proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait()
        proc.bench_log.close()


def stop(proc):
    if proc and proc.poll() is None:
        os.killpg(proc.pid, signal.SIGTERM)
        try:
            proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            os.killpg(proc.pid, signal.SIGKILL)
            proc.wait()
    if proc and getattr(proc, "bench_log", None):
        proc.bench_log.close()


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]





def table_manifest(source):
    with sqlite3.connect(f"file:{source}?mode=ro", uri=True) as db:
        manifest = []
        for table in TABLES:
            columns = [row[1] for row in db.execute(f'PRAGMA table_info("{table}")')]
            if not columns:
                raise ValueError("corpus lacks canonical table " + table)
            if table == "observers":
                columns.insert(0, "rowid")
            manifest.append(dict(name=table, columns=columns))
        return manifest


def validate(source, manifest, logical=False):
    result = {}
    with sqlite3.connect(f"file:{source}?mode=ro", uri=True) as db:
        for table in manifest:
            name = table["name"]
            columns = [c for c in table["columns"] if not logical or c not in VOLATILE.get(name, set())]
            select = ",".join('"' + c + '"' for c in columns)
            digest, count = hashlib.sha256(), 0
            for row in db.execute(f'SELECT {select} FROM "{name}" ORDER BY {select}'):
                digest.update(canonical_row(row))
                count += 1
            result[name] = dict(rows=count, sha256=digest.hexdigest())
    return result


class Resources:
    def __init__(self, path, budget, data_paths):
        self.path, self.budget, self.data_paths = path, budget, data_paths
        self.phase="prepare"
        self.done = threading.Event()
        self.error = None
        self.thread = threading.Thread(target=self.sample, daemon=True)

    def sample(self):
        try:
            self.sample_rows()
        except BaseException as error:
            self.error = error

    def sample_rows(self):
        with open(self.path, "w", newline="") as stream, open(self.path.parent/"processes.csv","w",newline="") as processes:
            process_writer=csv.DictWriter(processes,fieldnames=["mono_ns","phase","pid","command","rss_bytes","pss_bytes"])
            process_writer.writeheader()
            writer = csv.DictWriter(stream, fieldnames=["mono_ns", "phase", "filesystem_free_bytes", "cgroup_memory_bytes", "cgroup_memory_peak_bytes", "cgroup_cpu_usec", "rss_bytes", "pss_bytes", "storage_bytes", "wal_bytes", "io_stat", "memory_events"])
            writer.writeheader()
            while not self.done.is_set():
                row = dict(mono_ns=time.monotonic_ns(), phase=self.phase, filesystem_free_bytes=shutil.disk_usage(self.path.parent).free, cgroup_memory_bytes="not measured", cgroup_memory_peak_bytes="not measured", cgroup_cpu_usec="not measured", rss_bytes="not measured", pss_bytes="not measured", storage_bytes=0, wal_bytes=0, io_stat="not measured", memory_events="not measured")
                if self.budget.path:
                    c = self.budget.path
                    row["cgroup_memory_bytes"] = (c / "memory.current").read_text().strip()
                    peak = self.budget.memory_peak()
                    if peak is not None:
                        row["cgroup_memory_peak_bytes"] = peak
                    cpu = dict(line.split() for line in (c / "cpu.stat").read_text().splitlines())
                    row["cgroup_cpu_usec"] = cpu.get("usage_usec", "not measured")
                    row["io_stat"] = (c / "io.stat").read_text().strip()
                    row["memory_events"] = (c / "memory.events").read_text().strip()
                    rss = pss = 0
                    for pid in (c / "cgroup.procs").read_text().split():
                        try:
                            process_rss=process_pss=0
                            for line in Path("/proc", pid, "smaps_rollup").read_text().splitlines():
                                if line.startswith("Rss:"):
                                    process_rss += int(line.split()[1]) * 1024
                                if line.startswith("Pss:"):
                                    process_pss += int(line.split()[1]) * 1024
                            rss+=process_rss;pss+=process_pss
                            process_writer.writerow(dict(mono_ns=row["mono_ns"],phase=self.phase,pid=pid,command=Path("/proc",pid,"comm").read_text().strip(),rss_bytes=process_rss,pss_bytes=process_pss))
                        except (FileNotFoundError, PermissionError, ProcessLookupError):
                            pass
                    row.update(rss_bytes=rss, pss_bytes=pss)
                for root in self.data_paths:
                    for file in root.rglob("*") if root.is_dir() else [root, Path(str(root) + "-wal")]:
                        try:
                            if file.is_file():
                                size = file.stat().st_size
                                row["storage_bytes"] += size
                                if file.name.endswith("-wal"):
                                    row["wal_bytes"] += size
                        except FileNotFoundError:
                            pass
                writer.writerow(row)
                stream.flush()
                processes.flush()
                self.done.wait(1)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        try:
            self.done.set()
            self.thread.join(timeout=10)
            if self.thread.is_alive():
                raise TimeoutError("resource sampler failed to stop")
            if self.error:
                raise self.error
            if self.budget.path:
                c=self.budget.path
                write_json(self.path.parent/"resource-final.json",dict(memory_peak_bytes=self.budget.memory_peak(),cpu_stat=(c/"cpu.stat").read_text(),io_stat=(c/"io.stat").read_text(),memory_events=(c/"memory.events").read_text(),budget=self.budget.info))
        except BaseException as error:
            detail = record_resource_failure(self.path.parent/"resource-error.json", error, phase=self.phase,
                                             workload_error_type=exc_type.__name__ if exc_type else None)
            if exc_value is None:
                raise RuntimeError("resource sampler failed: " + json.dumps(detail, sort_keys=True)) from error
        finally:
            self.budget.close_memory_peak()
        return False


def wait_file(path, processes, seconds=120):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if path.exists():
            return
        if any(p.poll() is not None for p in processes):
            for proc in processes:
                if proc.poll() is not None and getattr(proc, "bench_log", None):
                    record_failure(proc.bench_log.name, proc.returncode)
            raise RuntimeError("benchmark worker exited before the start barrier")
        time.sleep(.05)
    raise TimeoutError("benchmark start barrier timeout")


def server_settings(variant, sqlite_path, state_dir, run_env):
    config = dict(hashChannels=[f"#bench-{i:02d}" for i in range(20)], dbPath=str(sqlite_path), port=free_port(), packetStore=dict(retentionHours=168, maxMemoryMB=PACKET_STORE_MIB, hotStartupHours=0), runtime=dict(maxMemoryMB=SERVER_MEMORY_MIB), clientRxCoverage=dict(enabled=True), userManagement=dict(enabled=False))
    env = dict(run_env, GOMEMLIMIT=f"{SERVER_MEMORY_MIB}MiB", ENABLE_PPROF="false")
    return config, env


def retained_counts(source, since_epoch):
    db = sqlite3.connect(f"file:{source}?mode=ro", uri=True)
    try:
        tx = db.execute("SELECT count(*) FROM transmissions WHERE last_seen >= ?", (since_epoch,)).fetchone()[0]
        obs = db.execute("SELECT count(*) FROM observations o JOIN transmissions t ON t.id=o.transmission_id WHERE t.last_seen >= ?", (since_epoch,)).fetchone()[0]
        return dict(transmissions=tx, observations=obs)
    finally:
        db.close()


def check_loaded_rows(health, expected):
    if health.get("loadedTx") != expected["transmissions"] or health.get("loadedObs") != expected["observations"]:
        raise RuntimeError("startup did not load the complete retained corpus: " + json.dumps(dict(actual=health, expected=expected)))


def startup(server, base_url, output, expected, launched_ns, hot_hours, retained_expected=None, coverage_scope="complete retained corpus"):
    start, first, ready = launched_ns, None, None
    deadline = time.monotonic() + 600
    last = None
    while time.monotonic() < deadline:
        if server.poll() is not None:
            record_failure(server.bench_log.name, server.returncode)
            raise RuntimeError("server exited during startup")
        try:
            with urllib.request.urlopen(base_url + "/api/healthz", timeout=5) as response:
                body = json.load(response)
                first = first or time.monotonic_ns()
                last = body
                if response.status == 200 and response.headers.get("X-CoreScope-Load-Status") == "ready":
                    ready = time.monotonic_ns()
                    break
        except urllib.error.HTTPError as error:
            first = first or time.monotonic_ns()
            last = {"ready":False,"status":error.code}
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError):
            pass
        time.sleep(.1)
    if ready is None:
        raise TimeoutError("server did not reach full health/readiness")
    full = ready
    perf = None
    while time.monotonic() < deadline:
        with urllib.request.urlopen(base_url + "/api/perf", timeout=30) as response:
            perf = json.load(response)
        store = perf.get("packetStore", {})
        if store.get("backgroundLoadFailed"):
            raise RuntimeError("background startup load failed")
        if not hot_hours or store.get("backgroundLoadComplete") is True:
            full = time.monotonic_ns()
            break
        time.sleep(.1)
    else:
        raise TimeoutError("background startup did not complete")
    initial_health=last
    with urllib.request.urlopen(base_url+"/api/healthz",timeout=30) as response:
        last=json.load(response)
    if not last or last.get("loadedTx",0) <= 0 or last.get("loadedObs",0) <= 0:
        raise RuntimeError("ready server did not load a meaningful corpus")
    if retained_expected is not None:
        check_loaded_rows(last, retained_expected)
    with urllib.request.urlopen(base_url + "/api/stats", timeout=30) as response:
        stats = json.load(response)
    result = dict(first_http_ns=first-start, ready_ns=ready-start, full_ready_ns=full-start, hot_startup_hours=hot_hours, initial_health=initial_health, health=last, stats=stats, perf=perf, durable_expected=expected, loaded_expected=retained_expected, retained_expected=retained_expected if coverage_scope == "complete retained corpus" else None, loaded_count_status=coverage_scope + " verified")
    write_json(output, result)
    return result


def verify_completed_pair(output, pair, events, event_hash):
    if sha256(events) != event_hash:
        raise RuntimeError("paired event stream changed during replay")
    for filename in ("startup.json", "startup-hot1.json"):
        coverage = []
        for variant in ("baseline", "candidate"):
            health = json.loads((output / f"pair-{pair:02d}" / variant / filename).read_text())["health"]
            coverage.append((health["loadedTx"], health["loadedObs"]))
        if coverage[0] != coverage[1]:
            raise RuntimeError("startup memory coverage differs between paired revisions")


def summarize(output):
    rows=[]
    def add(pair,variant,scenario,name,values,errors=0,drops=0,throughput=None,timing="service"):
        stats=percentiles(values)
        rows.append(dict(pair=pair,variant=variant,scenario=scenario,event_class=name,timing=timing,samples=stats["samples"],p50_ns=stats["p50"],p95_ns=stats["p95"],p99_ns=stats["p99"],maximum_ns=stats["maximum"],errors=errors,drops=drops,completed_per_second_including_drain=throughput))
    for directory in sorted(output.glob("pair-*/*")):
        if not directory.is_dir():continue
        pair,variant=directory.parent.name,directory.name
        for file,scenario in (("requests.jsonl","mixed-http"),("sql-queries.jsonl","sql-miss"),("ingest.jsonl","durable-ingest")):
            path=directory/file
            if not path.exists():continue
            groups={}
            for item in json_lines(path):
                if not item.get("measured"):continue
                group=groups.setdefault(item["class"],dict(values=[],queue=[],total=[],errors=0,drops=0,first=None,last=None))
                if item.get("dropped"):
                    group["drops"]+=1;continue
                if item.get("error") or item.get("status",200)!=(item.get("expected_status") or 200):
                    group["errors"]+=1;continue
                begun=item.get("dispatched_ns",item.get("started_ns"))
                group["values"].append(item["completed_ns"]-begun)
                scheduled=item.get("scheduled_ns") or begun
                if item.get("scheduled_ns"):
                    group["queue"].append(begun-scheduled)
                    group["total"].append(item["completed_ns"]-scheduled)
                group["first"]=min(group["first"] or scheduled,scheduled)
                group["last"]=max(group["last"] or item["completed_ns"],item["completed_ns"])
            for name,group in sorted(groups.items()):
                elapsed=(group["last"] or 0)-(group["first"] or 0)
                rate=len(group["values"])/(elapsed/1e9) if elapsed>0 else None
                add(pair,variant,scenario,name,group["values"],group["errors"],group["drops"],rate)
                if group["queue"]:
                    add(pair,variant,scenario,name,group["queue"],group["errors"],group["drops"],rate,timing="queue")
                    add(pair,variant,scenario,name,group["total"],group["errors"],group["drops"],rate,timing="end_to_end")
        for filename in ("startup.json","startup-hot1.json"):
            if (directory/filename).exists():
                obj=json.loads((directory/filename).read_text())
                for phase in ("first_http_ns","ready_ns","full_ready_ns"):
                    add(pair,variant,filename.removesuffix(".json"),phase,[obj[phase]])
        for filename,key in (("retention.json","elapsed_ns"),("retention-empty.json","elapsed_ns")):
            if (directory/filename).exists():
                obj=json.loads((directory/filename).read_text());add(pair,variant,filename.removesuffix(".json"),key,[obj[key]])
        if (directory/"handler.json").exists():
            obj=json.loads((directory/"handler.json").read_text());add(pair,variant,"handler-control","200-valid-envelopes",obj["samples_ns"])
        if (directory/"visibility.json").exists():
            obj=json.loads((directory/"visibility.json").read_text());add(pair,variant,"websocket","new-transmission-sentinels",obj["samples_ns"],obj["measured_missing"])
    if rows:
        with open(output/"summary.csv","w",newline="") as stream:
            writer=csv.DictWriter(stream,fieldnames=list(rows[0]));writer.writeheader();writer.writerows(rows)
    resource_rows=[]
    for path in sorted(output.glob("pair-*/*/resources.csv")):
        groups={}
        with open(path,newline="") as stream:
            for row in csv.DictReader(stream):groups.setdefault(row["phase"],[]).append(row)
        for phase,samples in groups.items():
            def numbers(key):return [int(r[key]) for r in samples if str(r.get(key,"")).isdigit()]
            resource_rows.append(dict(pair=path.parts[-3],variant=path.parts[-2],phase=phase,samples=len(samples),observed_cgroup_memory_peak_bytes=max(numbers("cgroup_memory_bytes"),default=None),observed_pss_peak_bytes=max(numbers("pss_bytes"),default=None),observed_storage_peak_bytes=max(numbers("storage_bytes"),default=None),observed_wal_peak_bytes=max(numbers("wal_bytes"),default=None),minimum_filesystem_free_bytes=min(numbers("filesystem_free_bytes"),default=None)))
    if resource_rows:
        with open(output/"summary-resources.csv","w",newline="") as stream:
            writer=csv.DictWriter(stream,fieldnames=list(resource_rows[0]));writer.writeheader();writer.writerows(resource_rows)
    paired=[]
    keys=sorted({(r["scenario"],r["event_class"],r["timing"]) for r in rows})
    for scenario,name,timing in keys:
        for metric in ("p50_ns","p95_ns","p99_ns","completed_per_second_including_drain"):
            ratios=[];absolute=[]
            for pair in sorted({r["pair"] for r in rows}):
                values={r["variant"]:r[metric] for r in rows if (r["pair"],r["scenario"],r["event_class"],r["timing"])==(pair,scenario,name,timing)}
                if set(values)=={"baseline","candidate"} and values["baseline"] is not None and values["baseline"] > 0 and values["candidate"] is not None and values["candidate"] >= 0:
                    ratios.append(values["candidate"]/values["baseline"]);absolute.append(dict(pair=pair,**values))
            if ratios:
                paired.append(dict(scenario=scenario,event_class=name,timing=timing,metric=metric,pairs=len(ratios),candidate_over_baseline_median=percentiles(ratios)["p50"],minimum=min(ratios),maximum=max(ratios),absolute_runs=absolute))
    write_json(output/"paired-summary.json",paired)
    return rows


def build(root, variant, binaries, env, logs):
    before = production_hash(root)
    replacements = {}
    for package in ("ingestor", "server"):
        templates = ["common_test.go", "profiles_test.go", "clock_linux_test.go", "clock_other_test.go", package + "_test.go"]
        if package == "ingestor":
            templates.append("sqlite_adapter_test.go")
        for template in templates:
            replacements[str(root / "cmd" / package / ("zz_benchmark_" + template))] = str(HERE / "overlays" / template)
    overlay = binaries / (variant + "-overlay.json")
    write_json(overlay, {"Replace": replacements})
    for package in ("ingestor", "server"):
        command(["go", "test", "-trimpath", "-overlay", overlay, "-c", "-o", binaries / (variant + "-" + package + ".test"), "."], cwd=root / "cmd" / package, env=env, log=logs / (variant + "-" + package + "-build.log"))
        command([binaries / (variant + "-" + package + ".test"), "-test.run=^TestCoreScopeBenchmarkControl", "-test.count=1", "-test.timeout=3m"], cwd=root / "cmd" / package, env=env, log=logs / (variant + "-" + package + "-controls.log"))
        if package == "ingestor":
            command([binaries / (variant + "-ingestor.test"), "-test.run=^TestCoreScopeBenchmark.+", "-test.count=1", "-test.timeout=3m"], cwd=root / "cmd" / package, env=env, log=logs / (variant + "-generator-controls.log"))
    command(["go", "build", "-trimpath", "-o", binaries / (variant + "-server"), "."], cwd=root / "cmd/server", env=env, log=logs / (variant + "-server-binary.log"))
    if production_hash(root) != before:
        raise RuntimeError("build changed production sources")
    return before


def worker(binary, config, mode, env, log, budget=None):
    cfg = dict(config, mode=mode)
    path = Path(config["control_dir"]) / (mode + "-config.json")
    write_json(path, cfg)
    test = "TestCoreScopeBenchmarkServer" if mode == "server" else "TestCoreScopeBenchmark"
    return spawn([binary, "-test.run=^" + test + "$", "-test.count=1", "-test.timeout=30m", "-test.v"], cwd=path.parent, env=dict(env, CORESCOPE_BENCH_CONFIG=str(path), ENABLE_PPROF="false"), log=log, budget=budget)


def verify_harness(repo, revision, here=None):
    """The executable harness may live on a different branch than either app."""
    here = Path(here or HERE)
    check_revisions(revision, revision)
    prefix = "scripts/sqlite-benchmark/"
    if command(["git", "rev-parse", revision + "^{commit}"], cwd=repo).strip() != revision:
        raise ValueError("harness revision did not resolve exactly")
    names = command(["git", "ls-tree", "-r", "--name-only", revision, "--", prefix], cwd=repo).splitlines()
    if any(p.is_symlink() for p in here.rglob("*")):
        raise ValueError("harness symlinks are not supported")
    expected = {name.removeprefix(prefix) for name in names}
    actual = {p.relative_to(here).as_posix() for p in here.rglob("*") if p.is_file() and "__pycache__" not in p.parts}
    if not expected or actual != expected:
        raise ValueError("executing harness files differ from the requested harness revision")
    digest = hashlib.sha256()
    for name in sorted(expected):
        text = command(["git", "show", revision + ":" + prefix + name], cwd=repo)
        if (here / name).read_text(encoding="utf-8") != text:
            raise ValueError("executing harness content differs from the requested harness revision: " + name)
        digest.update(name.encode() + b"\0" + text.encode())
    return digest.hexdigest()


def declared_coverage(name, transmissions, observations):
    if name != "L":
        if transmissions is not None or observations is not None:
            raise ValueError("S/B require complete retained counts; overrides are not accepted")
        return None
    if not transmissions or not observations or transmissions < 1 or observations < 1:
        raise ValueError("L needs predeclared positive loaded-transmission and loaded-observation counts")
    if transmissions > corpus(name)["transmissions"] or observations > corpus(name)["observations"]:
        raise ValueError("declared cache coverage exceeds the corpus")
    return dict(transmissions=transmissions, observations=observations)


def capacity_preflight(name, destination):
    minimum_disk = {"S": 4, "B": 12, "L": 128}[name] * 1024**3
    if shutil.disk_usage(destination).free < minimum_disk:
        raise ValueError("insufficient available disk for corpus safety guard (S:4/B:12/L:128 GiB)")
    memory = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
    available = int(memory["MemAvailable"].split()[0]) * 1024
    # Six GiB measured envelope plus coordinator/generator and host headroom.
    required = 8 * 1024**3
    if available < required:
        raise ValueError("insufficient currently available RAM for corpus guard")
    return dict(available_ram_bytes=available, available_disk_bytes=shutil.disk_usage(destination).free,
                minimum_ram_bytes=required, minimum_disk_bytes=minimum_disk)


def export_profile_tops(directory, binaries, variant, env, logs):
    captures = {}
    for process, binary in (("server", binaries / (variant + "-server.test")), ("ingestor", binaries / (variant + "-ingestor.test"))):
        status = directory / ".profiles" / process / "done.json"
        if not status.exists() or not json.loads(status.read_text()).get("complete"):
            raise RuntimeError("diagnostic profile capture failed: " + process)
        captures[process] = json.loads(status.read_text())
        for kind in ("cpu", "heap", "mutex", "block"):
            raw = directory / ".profiles" / process / (kind + ".pprof")
            if not raw.is_file() or raw.stat().st_size == 0:
                raise RuntimeError("diagnostic profile missing: " + process + "-" + kind)
            top = command(["go", "tool", "pprof", "-top", "-nodecount=40", binary, raw], env=env, log=logs / (variant + "-" + process + "-" + kind + "-pprof.log"))
            # Mapping filenames can retain the execution directory despite
            # -trimpath symbols. Only public top text is exported; raw profiles
            # stay private alongside the synthetic database and event file.
            for private in (str(directory.resolve()), str(binaries.resolve())):
                top = top.replace(private, "<benchmark>")
            top = re.sub(r"\b[A-Za-z]:[\\/]\S+|(?<![A-Za-z0-9_.])/(?:home|Users|tmp|private|runner)/\S+", "<private path>", top)
            output = directory / "profiles" / (process + "-" + kind + "-top.txt")
            output.parent.mkdir(exist_ok=True)
            output.write_text(top)
    write_json(directory / "profile-capture.json", dict(diagnostic_only=True, scope="measured window of separate diagnostic invocation", captures=captures, mutex_fraction=5, block_rate_ns=10000, heap_after_gc=True, raw_profiles_public=False, profiling_listener=False))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--baseline-sha", required=True)
    parser.add_argument("--candidate-sha", required=True)
    parser.add_argument("--harness-sha", required=True)
    parser.add_argument("--corpus", choices=("S", "B", "L"), default="B")
    parser.add_argument("--pairs", type=int, default=5)
    parser.add_argument("--profile", choices=("primary", "smoke", "diagnostic"), default="primary")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cgroup", default=os.environ.get("CORESCOPE_BENCH_CGROUP"))
    parser.add_argument("--go-version", default="1.27.2")
    parser.add_argument("--seed", type=int, default=20261008)
    parser.add_argument("--warmup", type=int)
    parser.add_argument("--seconds", type=int)
    parser.add_argument("--ingest-rate", type=int, default=50)
    parser.add_argument("--http-rate", type=int, default=20)
    parser.add_argument("--expected-loaded-transmissions", type=int)
    parser.add_argument("--expected-loaded-observations", type=int)
    args = parser.parse_args(argv)
    check_revisions(args.baseline_sha, args.candidate_sha)
    check_revisions(args.harness_sha, args.harness_sha)
    validate_profile(args.profile, args.pairs, args.cgroup)
    declared = declared_coverage(args.corpus, args.expected_loaded_transmissions, args.expected_loaded_observations)
    warmup = args.warmup if args.warmup is not None else (2 if args.profile == "smoke" else 60)
    seconds = args.seconds if args.seconds is not None else (10 if args.profile == "smoke" else 180)
    if warmup < 0 or seconds < 1 or args.ingest_rate < 1 or args.http_rate < 1 or (warmup + seconds) * max(args.ingest_rate, args.http_rate) > 1_000_000:
        raise ValueError("invalid workload duration or offered rate")
    if sys.version_info < (3, 12) or sys.platform != "linux":
        raise ValueError("measurement requires Linux and Python 3.12+; portable controls can run separately")
    if os.geteuid() == 0:
        raise ValueError("run as an unprivileged account with delegated cgroup permissions")
    if len(os.sched_getaffinity(0)) < 4 or os.sysconf("SC_PHYS_PAGES") * os.sysconf("SC_PAGE_SIZE") < 8 * 1024**3:
        raise ValueError("one host with at least four CPUs and 8 GiB RAM is required")
    for executable in ("git", "go", "cc"):
        if not shutil.which(executable):
            raise ValueError("missing prerequisite: " + executable)
    go_version = command(["go", "version"]).strip()
    if not re.search(r"\bgo" + re.escape(args.go_version) + r"\b", go_version):
        raise ValueError("Go version differs from common toolchain pin")
    harness_hash = verify_harness(args.repo.resolve(), args.harness_sha)
    output = safe_output(args.output)
    guards = capacity_preflight(args.corpus, output.parent)
    output.mkdir(mode=0o700)
    runtime, logs, binaries = output / ".runtime", output / "logs", output / ".runtime/binaries"
    for path in (runtime, logs, binaries):
        path.mkdir(exist_ok=True)
    budget = Budget(args.cgroup, args.profile in ("primary", "diagnostic") or args.corpus == "L")
    env = {k: v for k, v in os.environ.items() if not k.startswith(("CORESCOPE_", "PG", "PPROF_")) and k not in ("DB_PATH", "MQTT_BROKER", "MQTT_TOPIC", "GOFLAGS", "ENABLE_PPROF")}
    env.update(CGO_ENABLED="1", GOMAXPROCS="3", GOMEMLIMIT="512MiB", GOTOOLCHAIN="local", GOFLAGS="", ENABLE_PPROF="false")
    server = None
    manifest = dict(status="incomplete", engine="native CGO SQLite", baseline_sha=args.baseline_sha, candidate_sha=args.candidate_sha,
                    harness_source_sha=args.harness_sha, harness_sha256=harness_hash, harness_text_normalization="UTF-8 with LF line endings",
                    profile=args.profile, seed=args.seed, corpus=args.corpus, shape=corpus(args.corpus), firmware_reference=FIRMWARE,
                    host=dict(system=platform.system(), kernel=platform.release(), architecture=platform.machine(), cpus=os.cpu_count(), ram_bytes=os.sysconf("SC_PHYS_PAGES") * os.sysconf("SC_PAGE_SIZE")),
                    go_version=go_version, compiler=command(["cc", "--version"]).splitlines()[0], resource_budget=budget.info, capacity_guards=guards,
                    cache_policy=dict(startup="new process, OS cache warm", mixed="application warmup before measured requests", sql="channel application cache explicitly cleared; OS cache warm"),
                    workload=dict(warmup_seconds=warmup, measured_seconds=seconds, ingest_rate=args.ingest_rate, http_rate=args.http_rate, queue_limit=1024, http_concurrency=64, server_memory_mib=SERVER_MEMORY_MIB, ingestor_memory_mib=512, packet_store_mib=PACKET_STORE_MIB, retention_hours=168),
                    declared_loaded_counts=declared, profiling=args.profile == "diagnostic", comparison_eligible=False, order=pair_order(args.pairs))
    write_json(output / "manifest.json", manifest)
    try:
        try:
            budget.preflight()
        except OSError as error:
            detail = record_resource_failure(output / "resource-preflight.json", error, status="failed")
            raise RuntimeError("resource preflight failed: " + json.dumps(detail, sort_keys=True)) from error
        write_json(output / "resource-preflight.json", dict(status="passed", enforced=bool(budget.path), counters=list(RESOURCE_COUNTERS) if budget.path else []))
        roots = {}
        for variant, revision in (("baseline", args.baseline_sha), ("candidate", args.candidate_sha)):
            roots[variant] = runtime / variant
            archive(args.repo.resolve(), revision, roots[variant])
            manifest[variant + "_production_hash_before"] = build(roots[variant], variant, binaries, env, logs)
        manifest["comparison_kind"] = "A/A controller qualification" if manifest["baseline_production_hash_before"] == manifest["candidate_production_hash_before"] else "source-pinned SQLite before/after"
        epoch = int(time.time())
        canonical = runtime / "corpus.sqlite"
        prepare_dir = output / "corpus"
        prepare_dir.mkdir()
        base_config = dict(control_dir=str(runtime / "prepare-controls"), corpus_info=str(prepare_dir / "corpus.json"), corpus=args.corpus, shape=corpus(args.corpus), seed=args.seed, epoch=epoch, sqlite=str(canonical), output=str(prepare_dir), warmup=0, seconds=0, ingest_rate=args.ingest_rate, http_rate=args.http_rate)
        finish(worker(binaries / "baseline-ingestor.test", base_config, "prepare", env, logs / "prepare.log"), timeout=1800)
        tables = table_manifest(canonical)
        canonical_validation = validate(canonical, tables)
        for name in ("transmissions", "observations"):
            if canonical_validation[name]["rows"] != corpus(args.corpus)[name]:
                raise RuntimeError("canonical corpus counts differ from declared shape")
        write_json(prepare_dir / "validation.json", canonical_validation)
        manifest.update(epoch=epoch, canonical_file_sha256=sha256(canonical), table_manifest=tables)
        manifest["binary_sha256"] = {p.name: sha256(p) for p in binaries.iterdir() if p.is_file() and not p.name.endswith(".json")}
        write_json(output / "manifest.json", manifest)
        after_by_pair, paired_sources = {}, {}
        for pair, variant in pair_order(args.pairs):
            budget.reset()
            run_dir = output / f"pair-{pair:02d}" / variant
            run_dir.mkdir(parents=True)
            if shutil.disk_usage(output).free < max(2 * 1024**3, canonical.stat().st_size * 8):
                raise RuntimeError("insufficient measured-corpus disk headroom before restore")
            config_dir = runtime / f"config-{pair:02d}-{variant}"
            state_dir = config_dir / "state"
            state_dir.mkdir(parents=True)
            sqlite_path = config_dir / "telemetry.sqlite"
            if pair not in paired_sources:
                pair_source = runtime / f"pair-{pair:02d}.sqlite"
                shutil.copy2(canonical, pair_source)
                delta = int(time.time()) - epoch
                freshen(pair_source, delta)
                paired_validation = validate(pair_source, tables)
                paired_sources[pair] = pair_source, delta, paired_validation
                write_json(output / f"pair-{pair:02d}" / "corpus-offset.json", dict(receipt_epoch_offset_seconds=delta, wire_epoch_unchanged=epoch, sha256=sha256(pair_source), validation=paired_validation))
            pair_source, delta, paired_validation = paired_sources[pair]
            source_record = json.loads((output / f"pair-{pair:02d}" / "corpus-offset.json").read_text())
            if sha256(pair_source) != source_record["sha256"]:
                raise RuntimeError("paired source database changed before restore")
            command(["cp", "--reflink=never", pair_source, sqlite_path], budget=budget, log=run_dir / "restore-copy.log")
            config = dict(base_config, control_dir=str(config_dir / "controls"), events_file=str(runtime / f"pair-{pair:02d}-events.jsonl"), epoch=epoch + delta, wire_epoch=epoch, output=str(run_dir), sqlite=str(sqlite_path), state_dir=str(state_dir), warmup=warmup, seconds=seconds, start_file=str(run_dir / "start.json"))
            if not Path(config["events_file"]).exists():
                finish(worker(binaries / "baseline-ingestor.test", config, "events", env, run_dir / "event-generation.log"), timeout=600)
                write_json(output / f"pair-{pair:02d}" / "events.json", dict(sha256=sha256(config["events_file"]), rows=(warmup + seconds) * args.ingest_rate, source="one baseline-generated stream used unchanged by both revisions"))
            event_hash = json.loads((output / f"pair-{pair:02d}" / "events.json").read_text())["sha256"]
            if sha256(config["events_file"]) != event_hash:
                raise RuntimeError("paired event stream changed before replay")
            run_env = dict(env, CORESCOPE_INGESTOR_STATS=str(state_dir / "ingestor-stats.json"))
            with Resources(run_dir / "resources.csv", budget, [sqlite_path]) as resources:
                with sqlite3.connect(sqlite_path) as db:
                    db.execute("ANALYZE")
                pre = validate(sqlite_path, tables)
                if pre != paired_validation:
                    raise RuntimeError("initial revision state differs from canonical corpus")
                write_json(run_dir / "validation-before.json", pre)
                expected = declared or retained_counts(sqlite_path, int(time.time()) - 168 * 3600)
                coverage_scope = "predeclared bounded cache" if declared else "complete retained corpus"
                app_config, server_env = server_settings(variant, sqlite_path, state_dir, run_env)
                write_json(config_dir / "config.json", app_config)
                server_args = ["-config-dir", str(config_dir), "-public", str(roots[variant] / "public"), "-port", str(app_config["port"]), "-poll-ms", "1000"]
                config["base_url"] = f"http://127.0.0.1:{app_config['port']}"
                resources.phase = "startup_full"
                launched = time.monotonic_ns()
                server = spawn([binaries / (variant + "-server"), *server_args], cwd=roots[variant], env=server_env, log=run_dir / "server.log", budget=budget)
                startup(server, config["base_url"], run_dir / "startup.json", pre, launched, 0, expected, coverage_scope)
                stop(server)
                app_config["packetStore"]["hotStartupHours"] = 1
                write_json(config_dir / "config.json", app_config)
                resources.phase = "startup_hot"
                launched = time.monotonic_ns()
                if args.profile == "diagnostic":
                    server_config = dict(config, server_args=server_args, profile_dir=str(run_dir / ".profiles/server"))
                    server = worker(binaries / (variant + "-server.test"), server_config, "server", server_env, run_dir / "server-hot.log", budget)
                else:
                    server = spawn([binaries / (variant + "-server"), *server_args], cwd=roots[variant], env=server_env, log=run_dir / "server-hot.log", budget=budget)
                startup(server, config["base_url"], run_dir / "startup-hot1.json", pre, launched, 1, expected, coverage_scope)
                resources.phase = "sql_queries"
                finish(worker(binaries / (variant + "-server.test"), config, "queries", server_env, run_dir / "queries.log", budget), timeout=180)
                resources.phase = "diagnostic_mixed" if args.profile == "diagnostic" else "mixed_with_retention"
                client = worker(binaries / (variant + "-server.test"), config, "client", server_env, run_dir / "client.log")
                replay_config = dict(config, profile_dir=str(run_dir / ".profiles/ingestor") if args.profile == "diagnostic" else "")
                replay = worker(binaries / (variant + "-ingestor.test"), replay_config, "replay", run_env, run_dir / "replay.log", budget)
                try:
                    wait_file(run_dir / "client-ready", [client, replay, server])
                    wait_file(run_dir / "replay-ready", [client, replay, server])
                    write_json(run_dir / "start.json", dict(mono_ns=time.monotonic_ns() + 2_000_000_000))
                    finish(replay, timeout=warmup + seconds + 180)
                    finish(client, timeout=warmup + seconds + 180)
                finally:
                    stop(replay); stop(client)
                requests = list(json_lines(run_dir / "requests.jsonl"))
                if len(requests) != (warmup + seconds) * args.http_rate or any(item.get("error") for item in requests):
                    raise RuntimeError("HTTP schedules/errors failed full-stream verification, including warmup")
                observed = visibility(json_lines(run_dir / "ingest.jsonl"), json_lines(run_dir / "websocket.jsonl"))
                write_json(run_dir / "visibility.json", observed)
                if not observed["verified"]:
                    raise RuntimeError("durable sentinels missing from real WebSocket")
                if args.profile == "diagnostic":
                    wait_file(run_dir / ".profiles/server/done.json", [server])
                    export_profile_tops(run_dir, binaries, variant, env, logs)
                resources.phase = "retention_empty"
                finish(worker(binaries / (variant + "-ingestor.test"), config, "retention", run_env, run_dir / "retention.log", budget), timeout=600)
                stop(server); server = None
                resources.phase = "plans"
                finish(worker(binaries / (variant + "-ingestor.test"), config, "plans", run_env, run_dir / "plans.log", budget), timeout=180)
                resources.phase = "post_validation"
                post = validate(sqlite_path, tables, logical=True)
                write_json(run_dir / "validation-after.json", post)
                finish(worker(binaries / (variant + "-ingestor.test"), config, "handler", run_env, run_dir / "handler.log", budget), timeout=180)
                after_by_pair.setdefault(pair, {})[variant] = post
                if len(after_by_pair[pair]) == 2:
                    verify_completed_pair(output, pair, Path(config["events_file"]), event_hash)
                if len(after_by_pair[pair]) == 2 and after_by_pair[pair]["baseline"] != after_by_pair[pair]["candidate"]:
                    raise RuntimeError("post-replay logical parity differs between revisions")
            remove_working_directory(config_dir, runtime)
        for variant, root in roots.items():
            manifest[variant + "_production_hash_after"] = production_hash(root)
            if manifest[variant + "_production_hash_after"] != manifest[variant + "_production_hash_before"]:
                raise RuntimeError("production sources changed during measurement")
        if sha256(canonical) != manifest["canonical_file_sha256"]:
            raise RuntimeError("canonical source database changed during measurement")
        for pair, (pair_source, _, _) in paired_sources.items():
            source_record = json.loads((output / f"pair-{pair:02d}" / "corpus-offset.json").read_text())
            if sha256(pair_source) != source_record["sha256"]:
                raise RuntimeError("paired source database changed during measurement")
        manifest["source_databases_unchanged"] = True
        if verify_harness(args.repo.resolve(), args.harness_sha) != harness_hash:
            raise RuntimeError("harness changed during measurement")
        summary = summarize(output)
        manifest.update(status="completed", comparison_eligible=args.profile == "primary" and manifest["comparison_kind"] != "A/A controller qualification", measured_rows=len(summary), completed_pairs=args.pairs)
        write_json(output / "manifest.json", manifest)
        (output / "report.md").write_text("# CoreScope native SQLite before/after\n\nSee the exact source/harness refs, corpus, common settings, correctness gates and raw evidence in manifest.json. Diagnostic profiling runs and A/A controls establish no performance improvement. OS-cold behavior and long soak were not measured.\n")
    except BaseException as error:
        for child in reversed(PROCESSES): stop(child)
        try: summarize(output)
        except (OSError, ValueError, KeyError) as summary_error: manifest["partial_summary_error"] = str(summary_error)
        manifest.update(status="failed", comparison_eligible=False, error=str(error))
        write_json(output / "manifest.json", manifest)
        raise
    finally:
        for child in reversed(PROCESSES): stop(child)
        PROCESSES.clear(); stop(server)
        pending_error = sys.exc_info()[1]
        try:
            budget.close()
        except Exception as cleanup_error:
            manifest.update(status="failed", comparison_eligible=False, cleanup_error_type=type(cleanup_error).__name__, cleanup_errno=getattr(cleanup_error,"errno",None))
            write_json(output / "manifest.json", manifest)
            if pending_error is None:
                raise
        finally:
            public_bundle(output, args.repo)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError) as error:
        print("benchmark:", error, file=sys.stderr)
        sys.exit(1)

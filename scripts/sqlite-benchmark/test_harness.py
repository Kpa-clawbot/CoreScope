import importlib.util
import csv
import errno
import io
import json
import pathlib
import re
import tempfile
import types
import unittest
from unittest import mock

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bench", HERE / "run.py")
bench = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench)


class MemoryPeakFile(io.BytesIO):
    """Model cgroup v2's reset scope: one open file description."""
    def __init__(self, state, mode):
        super().__init__()
        self.state, self.binary, self.peak = state, "b" in mode, state.lifetime
        state.handles.append(self)

    def write(self, value):
        if self.state.reject_reset:
            raise OSError(errno.EINVAL, "reset unsupported")
        self.peak = self.state.current
        return len(value)

    def read(self, size=-1):
        value = b"" if self.tell() else str(self.peak).encode() + b"\n"
        self.seek(len(value), io.SEEK_CUR)
        return value if self.binary else value.decode()


class HarnessTests(unittest.TestCase):
    def test_default_common_rate_and_emitted_worker_configs(self):
        parse = bench.argparse.ArgumentParser.parse_args
        for extra, expected_rate in (([], 50), (["--ingest-rate", "100"], 100)):
            parsed = []
            def capture(parser, argv):
                parsed.append(parse(parser, argv))
                raise RuntimeError("stop after parsing")
            with mock.patch.object(bench.argparse.ArgumentParser, "parse_args", capture):
                with self.assertRaisesRegex(RuntimeError, "stop after parsing"):
                    bench.main(["--baseline-sha", "2" * 40, "--harness-sha", "3" * 40, "--candidate-sha", "1" * 40, "--output", "corescope-bench-test", *extra])
            args = parsed[0]
            self.assertEqual((args.ingest_rate, args.http_rate, args.pairs), (expected_rate, 20, 5))
            self.assertEqual((args.warmup, args.seconds), (None, None))
            # Exercise the actual JSON controls consumed by both compiled workers.
            emitted = []
            with tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                for backend in ("baseline", "candidate"):
                    controls = root / backend
                    controls.mkdir()
                    config = dict(control_dir=str(controls), warmup=60, seconds=180,
                                  ingest_rate=args.ingest_rate, http_rate=args.http_rate)
                    with mock.patch.object(bench, "spawn") as spawn:
                        bench.worker(root / (backend + "-ingestor.test"), config, "replay", {}, root / (backend + ".log"))
                    written = json.loads(pathlib.Path(spawn.call_args.kwargs["env"]["CORESCOPE_BENCH_CONFIG"]).read_text())
                    written.pop("control_dir")
                    emitted.append(written)
            self.assertEqual(emitted[0], emitted[1])
            self.assertEqual(emitted[0], dict(mode="replay", warmup=60, seconds=180,
                                             ingest_rate=expected_rate, http_rate=20))
            self.assertEqual((emitted[0]["warmup"] + emitted[0]["seconds"]) * emitted[0]["ingest_rate"],
                             12000 if expected_rate == 50 else 24000)


    def test_common_server_memory_profile_retains_b_and_replay_headroom(self):
        # Native baseline B qualification tracked 1221.7 MiB. The original
        # 100/s envelope conservatively bounds the current 50/s experiment too.
        required_store_mib = 1221.7 + 24000 * 8279 / 1048576
        settings = []
        for backend in ("baseline", "candidate"):
            with mock.patch.object(bench, "free_port", return_value=12345):
                config, env = bench.server_settings(backend, pathlib.Path("corpus.sqlite"), pathlib.Path("state"), {})
            self.assertGreater(config["packetStore"]["maxMemoryMB"], required_store_mib)
            self.assertEqual(config["packetStore"], {"retentionHours": 168, "maxMemoryMB": 2048, "hotStartupHours": 0})
            self.assertEqual(config["runtime"]["maxMemoryMB"], 3072)
            self.assertEqual(env["GOMEMLIMIT"], "3072MiB")
            self.assertEqual("dbPath" in config, True)
            config.pop("dbPath", None)
            settings.append((config, env))
        self.assertEqual(settings[0], settings[1], "capacity settings must be common to both engines")

    def test_build_runs_compiled_generator_controls(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            binaries, logs = root / "binaries", root / "logs"
            binaries.mkdir(); logs.mkdir()
            with mock.patch.object(bench, "production_hash", return_value="stable"), mock.patch.object(bench, "command") as command:
                bench.build(root, "baseline", binaries, {}, logs)
            controls = [call.args[0] for call in command.call_args_list if pathlib.Path(call.args[0][0]).name == "baseline-ingestor.test"]
            self.assertTrue(any("-test.run=^TestCoreScopeBenchmark.+" in args for args in controls), "new Go generator regressions must execute, not only compile")

    def resource_fixture(self, root, missing=None):
        group = root / "private-cgroup-path"
        group.mkdir()
        for name, value in {"memory.current": "0", "memory.events": "oom 0\n", "cpu.stat": "usage_usec 0\n",
                            "io.stat": "8:0 rbytes=1024 wbytes=2048\n", "cgroup.procs": ""}.items():
            if name != missing:
                (group / name).write_text(value)
        budget = bench.Budget(None, False)
        budget.path = group
        budget.info = {"enforced": True, "memory_peak_reset": False}
        budget.close = mock.Mock()
        run = root / "pair-00" / "sqlite"
        run.mkdir(parents=True)
        return budget, run

    def memory_peak_counter(self, budget, reject_reset=False):
        (budget.path / "memory.reclaim").touch()
        state = types.SimpleNamespace(current=0, lifetime=9000, handles=[], reject_reset=reject_reset)
        real_open = pathlib.Path.open
        def open_file(path, mode="r", *args, **kwargs):
            if path == budget.path / "memory.peak":
                return MemoryPeakFile(state, mode)
            return real_open(path, mode, *args, **kwargs)
        return state, mock.patch.object(pathlib.Path, "open", open_file)

    def test_memory_peak_is_reset_per_leg_for_samples_and_final_report(self):
        with tempfile.TemporaryDirectory() as directory:
            budget, run = self.resource_fixture(pathlib.Path(directory))
            state, counter = self.memory_peak_counter(budget)
            def sample_peak(value):
                state.current = value
                state.lifetime = max(state.lifetime, value)
                for handle in state.handles:
                    if not handle.closed:
                        handle.peak = max(handle.peak, value)
                resource = bench.Resources(run / "resources.csv", budget, [])
                with mock.patch.object(resource.done, "wait", side_effect=lambda _: resource.done.set()):
                    resource.sample()
                self.assertIsNone(resource.error)
                with open(run / "resources.csv", newline="") as stream:
                    row = next(csv.DictReader(stream))
                # Use the real final-write path after the sampler has stopped.
                with mock.patch.object(resource.thread, "join"):
                    resource.__exit__(None, None, None)
                final = json.loads((run / "resource-final.json").read_text())
                self.assertTrue(final["budget"]["memory_peak_reset"])
                self.assertEqual(int(row["cgroup_memory_peak_bytes"]), value)
                self.assertEqual(final["memory_peak_bytes"], value)
                self.assertTrue(all(handle.closed for handle in state.handles))
            with counter:
                budget.reset()
                sample_peak(3000)
                state.current = 0
                budget.reset()
                sample_peak(1000)

    def test_memory_peak_descriptor_closes_on_failure_and_unsupported_reset(self):
        for unsupported in (False, True):
            with self.subTest(unsupported=unsupported), tempfile.TemporaryDirectory() as directory:
                budget, run = self.resource_fixture(pathlib.Path(directory))
                state, counter = self.memory_peak_counter(budget, reject_reset=unsupported)
                with counter:
                    budget.reset()
                    self.assertEqual(budget.info["memory_peak_reset"], not unsupported)
                    with self.assertRaisesRegex(ValueError, "workload failure"):
                        with bench.Resources(run / "resources.csv", budget, []):
                            raise ValueError("workload failure")
                self.assertTrue(all(handle.closed for handle in state.handles))
                final = json.loads((run / "resource-final.json").read_text())
                self.assertEqual(final["memory_peak_bytes"], None if unsupported else 0)

    def test_memory_peak_descriptor_closes_if_no_resource_context_is_entered(self):
        with tempfile.TemporaryDirectory() as directory:
            budget, _ = self.resource_fixture(pathlib.Path(directory))
            state, counter = self.memory_peak_counter(budget)
            with counter, mock.patch.object(pathlib.Path, "rmdir"):
                budget.reset()
                first = state.handles[0]
                budget.reset()
                self.assertTrue(first.closed)
                bench.Budget.close(budget)
            self.assertTrue(all(handle.closed for handle in state.handles))

    def test_sampler_failure_identifies_counter_without_private_path(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            budget, run = self.resource_fixture(root, missing="io.stat")
            with self.assertRaisesRegex(RuntimeError, "io.stat"):
                with bench.Resources(run / "resources.csv", budget, []) as resource:
                    resource.thread.join(timeout=2)
            evidence = json.loads((run / "resource-error.json").read_text())
            self.assertEqual(evidence["error_type"], "FileNotFoundError")
            self.assertEqual(evidence["errno"], errno.ENOENT)
            self.assertEqual(evidence["counter"], "io.stat")
            self.assertNotIn("private-cgroup-path", json.dumps(evidence))
            bench.public_bundle(root)
            self.assertTrue((root / "public/pair-00/sqlite/resource-error.json").is_file())

    def test_sampler_failure_does_not_replace_workload_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            budget, run = self.resource_fixture(root, missing="io.stat")
            with self.assertRaisesRegex(ValueError, "independent workload failure"):
                with bench.Resources(run / "resources.csv", budget, []) as resource:
                    resource.thread.join(timeout=2)
                    raise ValueError("independent workload failure")
            self.assertEqual(json.loads((run / "resource-error.json").read_text())["counter"], "io.stat")

    def test_successful_resource_sample_keeps_io_metrics(self):
        with tempfile.TemporaryDirectory() as directory:
            budget, run = self.resource_fixture(pathlib.Path(directory))
            resource = bench.Resources(run / "resources.csv", budget, [])
            with mock.patch.object(resource.done, "wait", side_effect=lambda _: resource.done.set()):
                resource.sample()
            self.assertIsNone(resource.error)
            with open(run / "resources.csv", newline="") as stream:
                rows = list(csv.DictReader(stream))
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["io_stat"], "8:0 rbytes=1024 wbytes=2048")

    def test_final_resource_write_failure_does_not_replace_workload_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            budget, run = self.resource_fixture(pathlib.Path(directory))
            with mock.patch.object(bench, "write_json", side_effect=PermissionError(errno.EACCES, "private detail", str(run / "resource-final.json"))):
                with self.assertRaisesRegex(ValueError, "independent workload failure"):
                    with bench.Resources(run / "resources.csv", budget, []):
                        raise ValueError("independent workload failure")

    def test_missing_counter_fails_before_archiving_or_building(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            budget, _ = self.resource_fixture(root, missing="io.stat")
            output = root / "corescope-bench-preflight"
            def version(args, **kwargs):
                return "go version go1.27.2" if args[0] == "go" else "compiler"
            with mock.patch.object(bench.sys, "platform", "linux"), \
                    mock.patch.object(bench.os, "geteuid", return_value=1000, create=True), \
                    mock.patch.object(bench.os, "sched_getaffinity", return_value={0,1,2,3}, create=True), \
                    mock.patch.object(bench.os, "sysconf", return_value=1048576, create=True), \
                    mock.patch.object(bench.shutil, "which", return_value="tool"), \
                    mock.patch.object(bench, "capacity_preflight", return_value={}), \
                    mock.patch.object(bench, "verify_harness", return_value="verified"), \
                    mock.patch.object(bench, "command", side_effect=version), \
                    mock.patch.object(bench, "Budget", return_value=budget), \
                    mock.patch.object(bench, "archive") as archive, \
                    mock.patch.object(bench, "build") as build:
                with self.assertRaisesRegex(RuntimeError, "io.stat"):
                    bench.main(["--baseline-sha", "2" * 40, "--candidate-sha", "1" * 40, "--harness-sha", "3" * 40,
                                "--profile", "smoke", "--pairs", "1", "--corpus", "S", "--output", str(output)])
            archive.assert_not_called()
            build.assert_not_called()
            evidence = json.loads((output / "public/resource-preflight.json").read_text())
            self.assertEqual(evidence["counter"], "io.stat")
            self.assertFalse(json.loads((output / "public/manifest.json").read_text())["comparison_eligible"])





    def test_corpus_counts_are_exact(self):
        for name, transmissions, observations in [("S", 30_000, 90_000), ("B", 128_000, 2_048_000), ("L", 1_125_000, 18_000_000)]:
            shape = bench.corpus(name)
            self.assertEqual(shape["transmissions"], transmissions)
            self.assertEqual(sum(bench.fanout(name, i) for i in range(transmissions)), observations)

    def test_pair_order_is_serial_and_alternates(self):
        self.assertEqual(bench.pair_order(3), [(0, "baseline"), (0, "candidate"), (1, "candidate"), (1, "baseline"), (2, "baseline"), (2, "candidate")])

    def test_sparse_p99_is_not_reported_as_zero(self):
        self.assertIsNone(bench.percentiles([1, 2, 3])["p99"])
        self.assertIsNone(bench.percentiles([])["p50"])
        self.assertEqual(bench.percentiles(range(1, 1001))["p99"], 990)

    def test_logical_digest_distinguishes_null_and_empty_and_normalizes_numbers(self):
        self.assertNotEqual(bench.canonical_row([None]), bench.canonical_row([""]))
        self.assertEqual(bench.canonical_row([1]), bench.canonical_row([1.0]))
        self.assertNotEqual(bench.canonical_row(["1"]), bench.canonical_row([1]))

    def test_destinations_fail_closed(self):
        with self.assertRaises(ValueError):
            bench.safe_output(pathlib.Path("/tmp"))
        with tempfile.TemporaryDirectory() as directory:
            target = pathlib.Path(directory) / "corescope-bench-fixture"
            self.assertEqual(bench.safe_output(target), target.resolve())
            target.mkdir()
            with self.assertRaises(ValueError):
                bench.safe_output(target)

    def test_floating_or_wrong_baseline_is_rejected(self):
        with self.assertRaises(ValueError):
            bench.check_revisions("master", "1" * 40)
        with self.assertRaises(ValueError):
            bench.check_revisions("2" * 40, "HEAD")
        bench.check_revisions(bench.UPSTREAM, "1" * 40)

    def test_primary_requires_five_pairs_and_resource_controls(self):
        with self.assertRaises(ValueError):
            bench.validate_profile("primary", 1, None)
        with self.assertRaises(ValueError):
            bench.validate_profile("primary", 5, None)
        bench.validate_profile("smoke", 1, None)

    def test_epoch_shift_preserves_nanoseconds_and_nulls(self):
        self.assertEqual(bench.shift_timestamp("2026-10-08T12:00:00.123456789Z", 3600), "2026-10-08T13:00:00.123456789Z")
        self.assertEqual(bench.shift_timestamp("2026-10-08 12:00:00", -1), "2026-10-08 11:59:59")
        self.assertIsNone(bench.shift_timestamp(None, 1))
        with self.assertRaises(ValueError):
            bench.shift_timestamp("unexpected", 1)

    def test_public_bundle_excludes_databases_payloads_and_controls(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            (root / ".runtime").mkdir()
            (root / ".runtime" / "events.jsonl").write_text("private packet payload")
            (root / "manifest.json").write_text('{"path":"' + str(root).replace('\\', '\\\\') + '"}')
            run = root / "pair-00" / "sqlite"
            run.mkdir(parents=True)
            (run / "replay-config.json").write_text("private control")
            (run / "requests.jsonl").write_text('{"class":"nodes"}\n')
            bench.public_bundle(root)
            files = {p.relative_to(root / "public").as_posix() for p in (root / "public").rglob("*") if p.is_file()}
            self.assertEqual(files, {"manifest.json", "pair-00/sqlite/requests.jsonl"})

    def test_visibility_is_bound_to_durable_sent_hashes(self):
        sent = [{"hash":"a", "completed_ns":100, "new_transmission":True, "measured":True}, {"hash":"b", "completed_ns":200, "new_transmission":True, "measured":True}]
        result = bench.visibility(sent,[{"hash":"a","received_ns":90},{"hash":"a","received_ns":110},{"hash":"unrelated","received_ns":300}])
        self.assertFalse(result["verified"])
        self.assertEqual(result["missing_hashes"],["b"])
        self.assertEqual(result["duplicate_hash_receipts"],1)
        self.assertEqual(result["visible_before_ingest_return"],1)
        self.assertIsNone(result["lag_ns"]["p99"])

    def test_visibility_headline_excludes_warmup_but_keeps_earliest_negative_lags(self):
        sent = [{"hash": "warm", "completed_ns": 100, "new_transmission": True, "measured": False},
                {"hash": "measured", "completed_ns": 200, "new_transmission": True, "measured": True}]
        received = [{"hash": "warm", "received_ns": 90}, {"hash": "warm", "received_ns": 95},
                    {"hash": "warm", "received_ns": 99}, {"hash": "measured", "received_ns": 190},
                    {"hash": "measured", "received_ns": 220}]
        result = bench.visibility(sent, received)
        self.assertTrue(result["verified"])
        self.assertEqual(result["samples_ns"], [-10])
        self.assertEqual(result["lag_ns"]["samples"], 1)
        self.assertEqual(result["duplicate_hash_receipts"], 1)
        self.assertEqual(result["duplicate_hash_receipts_all"], 3)
        self.assertEqual(result["visible_before_ingest_return"], 1)
        self.assertEqual(result["visible_before_ingest_return_all"], 2)

    def test_missing_warmup_sentinel_still_fails_verification(self):
        sent = [{"hash": "warm", "completed_ns": 100, "new_transmission": True, "measured": False},
                {"hash": "measured", "completed_ns": 200, "new_transmission": True, "measured": True}]
        result = bench.visibility(sent, [{"hash": "measured", "received_ns": 230}])
        self.assertFalse(result["verified"])
        self.assertEqual(result["missing_hashes"], ["warm"])
        self.assertEqual(result["measured_missing"], 0)
        self.assertEqual(result["samples_ns"], [30])

    def test_warmup_does_not_make_sparse_visibility_p99_reportable(self):
        sent = [{"hash": str(i), "completed_ns": 100, "new_transmission": True, "measured": i == 1000} for i in range(1001)]
        result = bench.visibility(sent, [{"hash": str(i), "received_ns": 110} for i in range(1001)])
        self.assertEqual(result["lag_ns"]["samples"], 1)
        self.assertIsNone(result["lag_ns"]["p99"])

    def test_cgroup_launch_preserves_arguments_without_shell_interpolation(self):
        budget = bench.Budget.__new__(bench.Budget)
        budget.path = pathlib.Path("/sys/fs/cgroup/bench-test")
        arguments = ["/tmp/application", "$(must-not-execute)", "a b", "a'b"]
        launched = budget.arguments(arguments)
        self.assertEqual(launched[-len(arguments):], arguments)
        self.assertEqual(launched[-len(arguments)-1], str(budget.path))
        self.assertNotIn("must-not-execute", launched[2])
        budget.path = None
        self.assertEqual(budget.arguments(arguments), arguments)

    def test_failure_diagnostic_keeps_errors_but_excludes_sensitive_log_data(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            log = root / "replay.log"
            log.write_text('ordinary packet body: do not publish me\n'
                           '    zz_benchmark_ingestor_test.go:459: ERROR: missing relation "private_table" (SQLSTATE 42P01)\n'
                           '    zz_benchmark_ingestor_test.go:460: failed mqtt://writer:topsecret@private.example/db\n'
                           '    zz_benchmark_ingestor_test.go:461: failed at /home/private/state/config.json and C:\\Private\\state.json\n'
                           '    zz_benchmark_ingestor_test.go:462: payload={"text":"private body"} raw=00112233445566778899aabbccddeeff\n'
                           '    zz_benchmark_ingestor_test.go:463: host=private.example password=topsecret\n'
                           '--- FAIL: TestCoreScopeBenchmark (1.20s)\n')
            diagnostic = bench.record_failure(log, 1)
            artifact = root / "failure-replay.json"
            text = artifact.read_text()
            self.assertEqual(diagnostic["log"], "replay.log")
            self.assertEqual(diagnostic["exit_code"], 1)
            self.assertIn("SQLSTATE 42P01", text)
            self.assertIn("TestCoreScopeBenchmark", text)
            for private in ("do not publish me", "topsecret", "private.example", "/home/private", "Private", "private body", "00112233", "private_table"):
                self.assertNotIn(private, text)
            self.assertLess(len(text), 5000)

    def test_public_bundle_includes_only_sanitized_failure_artifact(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            logs = root / "logs"
            logs.mkdir()
            log = logs / "candidate-build.log"
            log.write_text("private raw log\n./main.go:27: undefined: MissingSymbol\n")
            bench.record_failure(log, 1)
            bench.public_bundle(root)
            self.assertTrue((root / "public/logs/failure-candidate-build.json").is_file())
            self.assertFalse((root / "public/logs/candidate-build.log").exists())

    def test_failure_diagnostic_bounds_long_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            log = pathlib.Path(directory) / "worker.log"
            log.write_text("ignored prefix\n" * 10000 + "panic: " + "x" * 20000 + "\n--- FAIL: TestCoreScopeBenchmark (1s)\n")
            result = bench.record_failure(log, 2)
            self.assertLessEqual(sum(len(x) for x in result["diagnostics"]), 2400)
            self.assertIn("--- FAIL: TestCoreScopeBenchmark (1s)", result["diagnostics"])


    def test_finish_records_failed_child_before_raising(self):
        with tempfile.TemporaryDirectory() as directory:
            log = pathlib.Path(directory) / "replay.log"
            log.write_text("    zz_benchmark_ingestor_test.go:460: durable effects did not match\n")
            child = types.SimpleNamespace(wait=lambda timeout: 1, poll=lambda: 1, bench_log=open(log, "a"))
            with self.assertRaisesRegex(RuntimeError, "failure-replay.json"):
                bench.finish(child)
            self.assertTrue(child.bench_log.closed)
            self.assertIn("durable effects did not match", (log.parent / "failure-replay.json").read_text())

    def test_retained_load_requires_every_eligible_row(self):
        with tempfile.TemporaryDirectory() as directory:
            source = pathlib.Path(directory) / "corpus.sqlite"
            with bench.sqlite3.connect(source) as db:
                db.executescript("CREATE TABLE transmissions(id INTEGER,last_seen INTEGER);"
                                 "CREATE TABLE observations(transmission_id INTEGER);"
                                 "INSERT INTO transmissions VALUES(1,90),(2,100),(3,110);"
                                 "INSERT INTO observations VALUES(1),(2),(2),(3);")
            db.close()
            expected = bench.retained_counts(source, 100)
            self.assertEqual(expected, {"transmissions": 2, "observations": 3})
            bench.check_loaded_rows({"loadedTx": 2, "loadedObs": 3}, expected)
            for incomplete in ({"loadedTx": 1, "loadedObs": 3}, {"loadedTx": 2, "loadedObs": 2}):
                with self.assertRaisesRegex(RuntimeError, "retained corpus"):
                    bench.check_loaded_rows(incomplete, expected)


if __name__ == "__main__":
    unittest.main()

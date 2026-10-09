import importlib.util
import pathlib
import tempfile
import types
import unittest

HERE = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("bench", HERE / "run.py")
bench = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench)


class HarnessTests(unittest.TestCase):
    def test_corpus_counts_are_exact(self):
        for name, transmissions, observations in [("S", 30_000, 90_000), ("B", 128_000, 2_048_000), ("L", 1_000_000, 16_000_000)]:
            shape = bench.corpus(name)
            self.assertEqual(shape["transmissions"], transmissions)
            self.assertEqual(sum(bench.fanout(name, i) for i in range(transmissions)), observations)

    def test_pair_order_is_serial_and_alternates(self):
        self.assertEqual(bench.pair_order(3), [(0, "sqlite"), (0, "postgres"), (1, "postgres"), (1, "sqlite"), (2, "sqlite"), (2, "postgres")])

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
            bench.check_revisions("2" * 40, "1" * 40)
        bench.check_revisions(bench.BASELINE, "1" * 40)

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
        sent = [{"hash":"a", "completed_ns":100, "new_transmission":True}, {"hash":"b", "completed_ns":200, "new_transmission":True}]
        result = bench.visibility(sent,[{"hash":"a","received_ns":90},{"hash":"a","received_ns":110},{"hash":"unrelated","received_ns":300}])
        self.assertFalse(result["verified"])
        self.assertEqual(result["missing_hashes"],["b"])
        self.assertEqual(result["duplicate_hash_receipts"],1)
        self.assertEqual(result["visible_before_ingest_return"],1)
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
                           '    zz_benchmark_ingestor_test.go:460: failed postgres://writer:topsecret@private.example/db\n'
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
            log = logs / "postgres-build.log"
            log.write_text("private raw log\n./main.go:27: undefined: MissingSymbol\n")
            bench.record_failure(log, 1)
            bench.public_bundle(root)
            self.assertTrue((root / "public/logs/failure-postgres-build.json").is_file())
            self.assertFalse((root / "public/logs/postgres-build.log").exists())

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

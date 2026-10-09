import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("sqlite_bench", HERE / "run.py")
bench = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bench)


class SQLiteControls(unittest.TestCase):
    def test_completed_pair_requires_unchanged_input_and_equal_cache_coverage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            events = root / "events.jsonl"
            events.write_text("synthetic input\n")
            digest = bench.sha256(events)
            for variant in ("baseline", "candidate"):
                run = root / "pair-00" / variant
                run.mkdir(parents=True)
                for filename in ("startup.json", "startup-hot1.json"):
                    bench.write_json(run / filename, {"health": {"loadedTx": 10, "loadedObs": 30}})
            bench.verify_completed_pair(root, 0, events, digest)
            changed = root / "pair-00/candidate/startup-hot1.json"
            bench.write_json(changed, {"health": {"loadedTx": 10, "loadedObs": 29}})
            with self.assertRaisesRegex(RuntimeError, "coverage differs"):
                bench.verify_completed_pair(root, 0, events, digest)
            bench.write_json(changed, {"health": {"loadedTx": 10, "loadedObs": 30}})
            events.write_text("changed input\n")
            with self.assertRaisesRegex(RuntimeError, "event stream changed"):
                bench.verify_completed_pair(root, 0, events, digest)

    def test_server_and_workers_force_the_http_profiler_off(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            _,env=bench.server_settings("baseline",root/"data.sqlite",root,{"ENABLE_PPROF":"true"})
            self.assertEqual(env["ENABLE_PPROF"],"false")
            with mock.patch.object(bench,"spawn") as spawn:
                bench.worker(root/"server.test",dict(control_dir=str(root)),"server",{"ENABLE_PPROF":"true"},root/"log")
            self.assertEqual(spawn.call_args.kwargs["env"]["ENABLE_PPROF"],"false")
            self.assertIn("-test.run=^TestCoreScopeBenchmarkServer$",spawn.call_args.args[0])

    def test_summary_keeps_queue_service_and_total_as_separate_distributions(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);run=root/"pair-00/baseline";run.mkdir(parents=True)
            events=[dict(**{"class":"request"},measured=True,status=200,scheduled_ns=10,dispatched_ns=20,completed_ns=30),
                    dict(**{"class":"request"},measured=True,status=200,scheduled_ns=100,dispatched_ns=120,completed_ns=160)]
            (run/"requests.jsonl").write_text("".join(json.dumps(row)+"\n" for row in events))
            summary=bench.summarize(root)
            self.assertEqual({row.get("timing") for row in summary},{"queue","service","end_to_end"})
            medians={row["timing"]:row["p50_ns"] for row in summary}
            self.assertEqual(medians,{"queue":10,"service":10,"end_to_end":20})

    def test_harness_ref_is_verified_independently_and_dirty_content_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            here = Path(directory)
            (here / "run.py").write_text("print('reviewed')\n")
            calls = []
            def git(args, **kwargs):
                calls.append(args)
                if args[1] == "rev-parse": return "4" * 40 + "\n"
                if args[1] == "ls-tree": return "scripts/sqlite-benchmark/run.py\n"
                return "print('reviewed')\n"
            with mock.patch.object(bench, "command", side_effect=git):
                digest = bench.verify_harness(here, "4" * 40, here)
                self.assertEqual(len(digest), 64)
                (here / "run.py").write_text("print('changed')\n")
                with self.assertRaisesRegex(ValueError, "content differs"):
                    bench.verify_harness(here, "4" * 40, here)
                (here / "extra.py").write_text("unexpected\n")
                with self.assertRaisesRegex(ValueError, "files differ"):
                    bench.verify_harness(here, "4" * 40, here)
            self.assertTrue(any("4" * 40 + ":scripts/sqlite-benchmark/run.py" in call for call in calls))

    def test_bounded_cache_targets_cannot_relax_s_or_b(self):
        for corpus in ("S", "B"):
            self.assertIsNone(bench.declared_coverage(corpus, None, None))
            with self.assertRaises(ValueError): bench.declared_coverage(corpus, 1, 1)
        for counts in ((None,None),(1,None),(0,1),(-1,1),(2_000_000,18_000_000)):
            with self.assertRaises(ValueError): bench.declared_coverage("L", *counts)
        self.assertEqual(bench.corpus("L")["observations"],18_000_000)
        self.assertEqual(bench.declared_coverage("L",100000,1600000),dict(transmissions=100000,observations=1600000))

    def test_capacity_guard_uses_actual_available_memory_and_disk(self):
        from types import SimpleNamespace
        with mock.patch.object(bench.shutil,"disk_usage",return_value=SimpleNamespace(free=129*1024**3)):
            with mock.patch.object(bench.Path,"read_text",return_value="MemAvailable: 9000000 kB\n"):
                self.assertGreater(bench.capacity_preflight("L",Path("."))["available_ram_bytes"],8*1024**3)
            with mock.patch.object(bench.Path,"read_text",return_value="MemAvailable: 1000000 kB\n"):
                with self.assertRaisesRegex(ValueError,"available RAM"):bench.capacity_preflight("L",Path("."))
        with mock.patch.object(bench.shutil,"disk_usage",return_value=SimpleNamespace(free=1024)):
            with self.assertRaisesRegex(ValueError,"available disk"):bench.capacity_preflight("B",Path("."))

    def test_diagnostic_is_separate_and_requires_a_common_budget(self):
        with self.assertRaises(ValueError):bench.validate_profile("diagnostic",1,None)
        with self.assertRaises(ValueError):bench.validate_profile("diagnostic",5,"owned")
        bench.validate_profile("diagnostic",1,"owned")

    def test_private_raw_profiles_are_not_published(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);run=root/"pair-00/baseline"
            private=run/".profiles/server";private.mkdir(parents=True)
            (private/"cpu.pprof").write_bytes(b"private raw profile")
            public=run/"profiles";public.mkdir()
            (public/"server-cpu-top.txt").write_text("safe module.function\n")
            bench.public_bundle(root)
            self.assertFalse((root/"public/pair-00/baseline/.profiles").exists())
            self.assertEqual((root/"public/pair-00/baseline/profiles/server-cpu-top.txt").read_text(),"safe module.function\n")

    def test_two_native_variants_alternate(self):
        self.assertEqual(bench.pair_order(2), [(0, "baseline"), (0, "candidate"), (1, "candidate"), (1, "baseline")])

    def test_baseline_and_candidate_are_independent_exact_revisions(self):
        bench.check_revisions("2" * 40, "3" * 40)
        bench.check_revisions("2" * 40, "2" * 40)  # A/A controller qualification.
        for left, right in (("master", "3" * 40), ("2" * 40, "HEAD")):
            with self.assertRaises(ValueError):
                bench.check_revisions(left, right)

    def test_both_variants_build_the_native_adapter_with_trimmed_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binaries, logs = root / "bin", root / "logs"
            binaries.mkdir(); logs.mkdir()
            for variant in ("baseline", "candidate"):
                with mock.patch.object(bench, "production_hash", return_value="stable"), mock.patch.object(bench, "command") as command:
                    bench.build(root, variant, binaries, {}, logs)
                overlay = json.loads((binaries / (variant + "-overlay.json")).read_text())
                adapters = [p for p in overlay["Replace"].values() if p.endswith("_adapter_test.go")]
                self.assertEqual(adapters, [str(HERE / "overlays/sqlite_adapter_test.go")])
                builds = [call.args[0] for call in command.call_args_list if call.args[0][0] == "go"]
                self.assertEqual(len(builds), 3)
                for args in builds:
                    self.assertIn("-trimpath", args)
                self.assertFalse(any("migrate" in str(arg) for args in builds for arg in args))


if __name__ == "__main__":
    unittest.main()

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import runpy
import subprocess
import sys
import unittest
from unittest import mock

sys.dont_write_bytecode = True
PATH = Path(__file__).with_name("postgres-connect.py")
SPEC = importlib.util.spec_from_file_location("postgres_connect", PATH)
CONNECT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CONNECT)


class ConnectionTests(unittest.TestCase):
    def test_url_decodes_credentials_into_environment_and_retains_tls(self):
        env = CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL":
            "postgresql://qa_reader:p%40ss%2Fword@[::1]:5444/measurements?sslmode=verify-full&sslrootcert=%2Fprivate%2Fca.pem",
            "PGSERVICE": "unrelated", "PGOPTIONS": "-c role=owner"})
        self.assertEqual((env["PGHOST"], env["PGPORT"], env["PGUSER"], env["PGDATABASE"]), ("::1", "5444", "qa_reader", "measurements"))
        self.assertEqual(env["PGPASSWORD"], "p@ss/word")
        self.assertEqual((env["PGSSLMODE"], env["PGSSLROOTCERT"]), ("verify-full", "/private/ca.pem"))
        self.assertNotIn("PGSERVICE", env)
        self.assertNotIn("PGOPTIONS", env)

    def test_url_without_tls_override_preserves_private_tls_setting(self):
        env = CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL": "postgres://reader@localhost/data", "PGSSLMODE": "verify-full"})
        self.assertEqual(env["PGSSLMODE"], "verify-full")

    def test_raw_query_semicolons_cannot_hide_tls_parameters(self):
        for query in ("application_name=qa;sslmode=verify-full",
                      "application_name=qa&sslrootcert=ca;sslmode=verify-full"):
            with self.subTest(query=query), self.assertRaises(ValueError):
                CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL": "postgres://reader@localhost/data?" + query})

    def test_encoded_semicolon_is_a_value_and_preserves_explicit_tls(self):
        env = CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL":
            "postgres://reader@localhost/data?application_name=qa%3Bsslmode%3Ddisable&sslmode=verify-full"})
        self.assertEqual(env["PGAPPNAME"], "qa;sslmode=disable")
        self.assertEqual(env["PGSSLMODE"], "verify-full")

    def test_explicit_url_clears_inherited_host_address(self):
        source = {"CORESCOPE_READER_DATABASE_URL": "postgres://reader@localhost:5444/data",
                  "PGHOST": "unrelated-profile", "PGHOSTADDR": "stale-address"}
        env = CONNECT.connection_env(source)
        self.assertEqual((env["PGHOST"], env["PGPORT"]), ("localhost", "5444"))
        self.assertNotIn("PGHOSTADDR", env)
        self.assertEqual(source["PGHOSTADDR"], "stale-address")
        self.assertEqual(CONNECT.connection_env({"PGDATABASE": "data", "PGHOSTADDR": "private-address"})["PGHOSTADDR"], "private-address")

    def test_omitted_url_port_uses_environment_or_default(self):
        for inherited, expected in ((None, "5432"), ("", "5432"), ("25432", "25432"), ("1", "1"), ("65535", "65535")):
            source = {"CORESCOPE_READER_DATABASE_URL": "postgres://reader@localhost/data"}
            if inherited is not None:
                source["PGPORT"] = inherited
            with self.subTest(inherited=inherited):
                self.assertEqual(CONNECT.connection_env(source)["PGPORT"], expected)

    def test_explicit_url_port_overrides_even_invalid_inherited_port(self):
        for port in (1, 5444, 65535):
            env = CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL": f"postgres://reader@localhost:{port}/data", "PGPORT": "invalid-inherited"})
            self.assertEqual(env["PGPORT"], str(port))

    def test_invalid_effective_url_port_is_rejected_safely(self):
        for port in ("0", "65536", "-1", "invalid-secret", " 5432", "5432.0"):
            for explicit in (False, True):
                url = f"postgres://reader:secret-sentinel@localhost{':' + port if explicit else ''}/data"
                with self.subTest(explicit=explicit, port=port):
                    with self.assertRaises(ValueError) as raised:
                        CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL": url, "PGPORT": "5432" if explicit else port})
                    self.assertNotIn("secret", str(raised.exception))
                    self.assertNotIn(port, str(raised.exception))

    def test_search_path_escapes_option_delimiters(self):
        env = CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL":
            "postgres://reader@localhost/data?search_path=qa%2C%20%22space%20%5C%20-crole%3Downer%22"})
        self.assertEqual(env["PGOPTIONS"], '-c search_path=qa,\\ "space\\ \\\\\\ -crole=owner"')

    def test_native_private_settings_need_no_url(self):
        self.assertEqual(CONNECT.connection_env({"PGSERVICE": "qa-reader", "PGPASSFILE": "/private/pass"})["PGPASSFILE"], "/private/pass")
        self.assertEqual(CONNECT.connection_env({"PGDATABASE": "data", "PGUSER": "reader"})["PGDATABASE"], "data")
        with self.assertRaises(ValueError):
            CONNECT.connection_env({"PGDATABASE": "postgres://reader:secret@localhost/data"})

    def test_invalid_urls_reject_without_exposing_values(self):
        for url in ("postgres://u:secret@/db", "postgres://u:secret@a,b/db", "postgres://u:secret@localhost/",
                    "postgres://u:secret@localhost/db?unsupported=yes", "postgres://u:secret@localhost/db?sslmode=require&sslmode=disable",
                    "postgres://u:secret@localhost/db?sslmode=", "postgres://u:secret@localhost/db?sslmode=invalid",
                    "postgres://u:secret%xx@localhost/db", "postgres://u:secret@localhost/db\n", "postgres://u:secret@localhost:bad/db"):
            with self.subTest(url_kind=url.split("?")[0].split(":")[-1]):
                with self.assertRaises(ValueError) as raised:
                    CONNECT.connection_env({"CORESCOPE_READER_DATABASE_URL": url})
                self.assertNotIn("secret", str(raised.exception))
                self.assertNotIn(url, str(raised.exception))

    def test_psql_argv_never_contains_credentials(self):
        url = "postgres://reader:secret-sentinel@localhost/data?sslmode=verify-full"
        with mock.patch.dict(os.environ, {"CORESCOPE_READER_DATABASE_URL": url}, clear=True), \
                mock.patch.object(subprocess, "run", return_value=mock.Mock(returncode=0)) as run:
            with self.assertRaises(SystemExit) as exited:
                runpy.run_path(str(PATH), run_name="__main__")
            self.assertEqual(exited.exception.code, 0)
            argv = run.call_args.args[0]
            self.assertNotIn(url, argv)
            self.assertNotIn("secret-sentinel", " ".join(argv))
            self.assertEqual(run.call_args.kwargs["env"]["PGPASSWORD"], "secret-sentinel")

    def test_malformed_url_error_is_safe_for_job_output(self):
        stream = io.StringIO()
        with mock.patch.dict(os.environ, {"CORESCOPE_READER_DATABASE_URL": "postgres://u:secret-sentinel@/db"}, clear=True), contextlib.redirect_stderr(stream):
            with self.assertRaises(SystemExit):
                runpy.run_path(str(PATH), run_name="__main__")
        self.assertNotIn("secret-sentinel", stream.getvalue())
        self.assertIn("invalid PostgreSQL reader URL", stream.getvalue())

    def test_libpq_errors_cannot_echo_connection_values(self):
        stream = io.StringIO()
        def fail(*args, **kwargs):
            diagnostic = 'psql: invalid connection setting "secret-sentinel"\n'
            if kwargs.get("stderr") != subprocess.PIPE:
                print(diagnostic, file=sys.stderr)
            return mock.Mock(returncode=2, stderr=diagnostic)
        with mock.patch.dict(os.environ, {"PGDATABASE": "data"}, clear=True), \
                mock.patch.object(subprocess, "run", side_effect=fail), contextlib.redirect_stderr(stream):
            with self.assertRaises(SystemExit):
                runpy.run_path(str(PATH), run_name="__main__")
        self.assertNotIn("secret-sentinel", stream.getvalue())
        self.assertIn("private connection settings", stream.getvalue())


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Run psql with connection credentials exclusively in private libpq variables.

PGDATABASE does not expand a URL. This QA launcher accepts the application's
reader URL, including search_path, or preconfigured native libpq settings.
Queries arrive on stdin. Their caller is responsible for checking reader grants.
"""
import os
import re
import subprocess
import sys
import urllib.parse


def connection_env(source):
    env = dict(source)
    url = env.get("CORESCOPE_READER_DATABASE_URL", "")
    if url:
        try:
            if re.search(r"%(?![0-9a-fA-F]{2})|[\x00-\x20\x7f]", url):
                raise ValueError()
            parsed = urllib.parse.urlsplit(url)
            if ";" in parsed.query:
                raise ValueError()
            host = urllib.parse.unquote(parsed.hostname or "")
            if parsed.scheme not in ("postgres", "postgresql") or not host or "," in host or not parsed.username or not parsed.path.strip("/") or parsed.fragment:
                raise ValueError()
            port = parsed.port
            if port is None:
                port = env.get("PGPORT") or "5432"
            if not re.fullmatch(r"[0-9]+", str(port)) or not 1 <= int(port) <= 65535:
                raise ValueError()
            env.update(PGHOST=host, PGPORT=str(int(port)),
                       PGUSER=urllib.parse.unquote(parsed.username),
                       PGDATABASE=urllib.parse.unquote(parsed.path[1:]))
            if parsed.password is not None:
                env["PGPASSWORD"] = urllib.parse.unquote(parsed.password)
            env.pop("PGSERVICE", None)
            env.pop("PGHOSTADDR", None)
            env.pop("PGSERVICEFILE", None)
            env.pop("PGOPTIONS", None)
            settings = {"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT",
                        "sslkey": "PGSSLKEY", "sslpassword": "PGSSLPASSWORD", "connect_timeout": "PGCONNECT_TIMEOUT",
                        "target_session_attrs": "PGTARGETSESSIONATTRS", "application_name": "PGAPPNAME"}
            seen = set()
            for key, value in urllib.parse.parse_qsl(parsed.query, keep_blank_values=True, strict_parsing=True):
                if key in seen:
                    raise ValueError()
                seen.add(key)
                if key == "search_path":
                    env["PGOPTIONS"] = "-c search_path=" + re.sub(r"([\\\s])", r"\\\1", value)
                elif key in settings:
                    if key == "sslmode" and value not in ("disable", "allow", "prefer", "require", "verify-ca", "verify-full"):
                        raise ValueError()
                    env[settings[key]] = value
                else:
                    raise ValueError()
        except (ValueError, UnicodeError):
            raise ValueError("invalid PostgreSQL reader URL; check its private configuration") from None
    elif not env.get("PGSERVICE") and not env.get("PGDATABASE"):
        raise ValueError("configure CORESCOPE_READER_DATABASE_URL or private libpq database/service settings")
    elif "://" in env.get("PGDATABASE", ""):
        raise ValueError("PGDATABASE requires a database name; put the URL in CORESCOPE_READER_DATABASE_URL")
    try:
        env["PGCONNECT_TIMEOUT"] = str(min(10, max(1, int(env.get("PGCONNECT_TIMEOUT", "10")))))
    except ValueError:
        raise ValueError("invalid PostgreSQL connection timeout") from None
    env["PGCLIENTENCODING"] = "UTF8"
    env.setdefault("PGAPPNAME", "corescope-qa")
    return env


if __name__ == "__main__":
    try:
        env = connection_env(os.environ)
        result = subprocess.run(["psql", "-X", "-qAt", "-w", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=sqlstate"],
                                env=env, stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace")
        if result.returncode:
            # libpq parse/connection errors can quote an invalid setting value.
            # Keep server error classes; never relay those arbitrary strings.
            codes = re.findall(r"^(?:ERROR|FATAL):\s+([0-9A-Z]{5})\s*$", result.stderr, re.MULTILINE)
            if codes:
                for code in codes[-3:]:
                    print("ERROR: " + code, file=sys.stderr)
            else:
                print("PostgreSQL client failed; check private connection settings and PostgreSQL 18 client availability", file=sys.stderr)
        sys.exit(result.returncode)
    except ValueError as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
    except OSError:
        print("psql is unavailable; install the PostgreSQL 18 client", file=sys.stderr)
        sys.exit(127)

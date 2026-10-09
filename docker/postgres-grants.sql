\set ON_ERROR_STOP on
-- Execute as the migration owner after schema installation/import, once in
-- each database. Runtime credentials never own objects or change readiness.
SELECT kind FROM corescope_schema \gset
SELECT :'kind' = 'telemetry' AS telemetry \gset
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
\if :telemetry
GRANT USAGE ON SCHEMA public TO corescope_reader, corescope_writer;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO corescope_reader;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO corescope_reader;
GRANT SELECT ON corescope_schema TO corescope_writer;
GRANT SELECT ON packets_v TO corescope_writer;
SELECT format('GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE %I.%I TO corescope_writer',schemaname,tablename)
FROM pg_tables WHERE schemaname='public' AND tablename NOT LIKE 'corescope\_%' ESCAPE '\' \gexec
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO corescope_writer;
\else
GRANT USAGE ON SCHEMA public TO corescope_accounts, corescope_channels;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO corescope_accounts;
SELECT format('GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE %I.%I TO corescope_accounts',schemaname,tablename)
FROM pg_tables WHERE schemaname='public' AND tablename NOT LIKE 'corescope\_%' ESCAPE '\' AND tablename <> 'schema_version' \gexec
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO corescope_accounts;
GRANT SELECT ON approved_channels,corescope_schema TO corescope_channels;
\endif

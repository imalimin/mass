-- D01 readiness will execute this with the application account and separately
-- verify the goose schema version. Query results never imply TOS readiness.
SELECT VERSION() AS server_version, @@session.time_zone AS session_time_zone;

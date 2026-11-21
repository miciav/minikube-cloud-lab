#!/bin/sh
set -eu
export STUDENT_PASSWORD="$(cat /run/bootstrap/student)"
psql --set ON_ERROR_STOP=1 --username postgres --dbname course <<'SQL'
\getenv password STUDENT_PASSWORD
CREATE ROLE student LOGIN PASSWORD :'password';
GRANT CREATE ON SCHEMA public TO student;
SQL

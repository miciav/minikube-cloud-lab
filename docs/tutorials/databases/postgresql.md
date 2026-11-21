# Connect to a managed PostgreSQL database

Start in the configured shell. The module provisions the database and a student login. Use psql through the lab wrapper:

```sh
lab install database
lab client database -c 'CREATE TABLE measurements(id integer PRIMARY KEY, value numeric)'
lab client database -c 'INSERT INTO measurements VALUES (1, 3.5)'
lab uninstall database
lab install database
lab client database -c 'SELECT * FROM measurements'
```

The row survives reinstall. For a host application use database `course`, login `student`, host `127.0.0.1`, port `15432` and the password in the student file in the private configuration directory printed by install. The role can create tables in the public schema. Keep the administrative password in the bootstrap directory.

Remove the lesson table with `lab client database -c 'DROP TABLE measurements'` and run `lab uninstall database`.

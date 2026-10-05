# Postgres example

Needs a local Postgres with a `myapp` database and the `pg_dump`/`pg_restore` tools.

    createdb myapp
    agentbox new a && agentbox new b
    agentbox ls        # databases myapp_agentbox_a and myapp_agentbox_b, cloned from a snapshot of myapp

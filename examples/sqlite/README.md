# SQLite example

    sqlite3 dev.db 'CREATE TABLE items(name TEXT);'   # any SQLite file works
    agentbox new a && agentbox new b
    agentbox ls        # each box has its own APP_PORT and its own copy of dev.db

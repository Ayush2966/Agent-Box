# MongoDB example

Needs a MongoDB server with a `myapp` database and the MongoDB Database Tools
(`mongodump`/`mongorestore`) on PATH. A local server or an Atlas cluster both work.

    mongosh myapp --eval 'db.items.insertOne({name: "one"})'
    agentbox new a && agentbox new b
    agentbox ls        # databases myapp_agentbox_a and myapp_agentbox_b, each a fresh dump of myapp

Each box's `MONGO_URL` points at its own database copy; the source `myapp` is never touched.

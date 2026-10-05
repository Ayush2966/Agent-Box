// Tiny app that reports which box it runs in and whether its own database copy exists.
const http = require('http');
const fs = require('fs');

const port = Number(process.env.APP_PORT || 3000);
const dbPath = process.env.DATABASE_PATH || 'dev.db';

http
  .createServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ box: process.env.AGENTBOX_NAME || null, db: dbPath, exists: fs.existsSync(dbPath) }));
  })
  .listen(port, '127.0.0.1', () => console.log(`listening on ${port}`));

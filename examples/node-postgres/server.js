// Tiny app that reports which box it runs in and which database it was given.
const http = require('http');

const port = Number(process.env.APP_PORT || 3000);
const db = new URL(process.env.DATABASE_URL).pathname.slice(1);

http
  .createServer((req, res) => {
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ box: process.env.AGENTBOX_NAME || null, db }));
  })
  .listen(port, '127.0.0.1', () => console.log(`listening on ${port}`));

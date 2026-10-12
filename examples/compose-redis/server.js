// Tiny app: every request increments a counter in this box's own Redis and reports it.
const http = require('http');
const net = require('net');

const port = Number(process.env.APP_PORT || 3000);
const redisPort = Number(process.env.REDIS_PORT || 6379);

function incr(key) {
  return new Promise((resolve, reject) => {
    const sock = net.connect(redisPort, '127.0.0.1');
    sock.on('error', reject);
    sock.on('data', (data) => {
      const m = /^:(\d+)/.exec(data.toString());
      sock.end();
      m ? resolve(Number(m[1])) : reject(new Error(data.toString()));
    });
    sock.write(`*2\r\n$4\r\nINCR\r\n$${key.length}\r\n${key}\r\n`);
  });
}

http
  .createServer(async (req, res) => {
    res.setHeader('content-type', 'application/json');
    try {
      const hits = await incr('hits');
      res.end(JSON.stringify({ box: process.env.AGENTBOX_NAME || null, redisPort, hits }));
    } catch (e) {
      res.statusCode = 500;
      res.end(JSON.stringify({ error: e.message }));
    }
  })
  .listen(port, '127.0.0.1', () => console.log(`listening on ${port}`));

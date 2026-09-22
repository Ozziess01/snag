// Записывает запросы настоящих SDK Sentry как фикстуры для тестов приёма.
//
//   node server.mjs ../../internal/ingest/testdata/sdk
//
// Порт 9911 — приём (DSN вида http://<sdk>@127.0.0.1:9911/1), ключ в DSN
// называет SDK и становится именем файла. Порт 9912 отдаёт browser.html:
// страница с другого origin, чтобы браузер прислал Origin, как в жизни.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';

const OUT = process.argv[2];
if (!OUT) throw new Error('укажите папку для фикстур');
fs.mkdirSync(OUT, { recursive: true });

const KEEP_HEADERS = ['content-type', 'content-encoding', 'x-sentry-auth', 'authorization', 'user-agent', 'origin'];
const counters = {};

function decode(body, encoding) {
  switch ((encoding || '').toLowerCase()) {
    case 'gzip': return zlib.gunzipSync(body);
    case 'deflate': return zlib.inflateSync(body);
    case 'br': return zlib.brotliDecompressSync(body);
    default: return body;
  }
}

function keyOf(req, url, body) {
  const m = /sentry_key=([^,\s&]+)/.exec(req.headers['x-sentry-auth'] || req.headers['authorization'] || '');
  if (m) return m[1];
  if (url.searchParams.get('sentry_key')) return url.searchParams.get('sentry_key');
  try {
    const first = decode(body, req.headers['content-encoding']).toString('utf8').split('\n', 1)[0];
    const dsn = JSON.parse(first).dsn;
    if (dsn) return new URL(dsn).username;
  } catch {}
  return 'unknown';
}

http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    const cors = { 'access-control-allow-origin': req.headers.origin || '*', 'access-control-allow-headers': '*' };
    if (req.method === 'OPTIONS') { res.writeHead(204, cors); res.end(); return; }

    const body = Buffer.concat(chunks);
    const url = new URL(req.url, 'http://capture');
    const key = keyOf(req, url, body);
    const n = (counters[key] = (counters[key] || 0) + 1);
    const name = `${key}-${String(n).padStart(2, '0')}`;
    const headers = Object.fromEntries(KEEP_HEADERS.filter((h) => req.headers[h]).map((h) => [h, req.headers[h]]));

    fs.writeFileSync(path.join(OUT, `${name}.body`), body);
    fs.writeFileSync(path.join(OUT, `${name}.json`), JSON.stringify({ method: req.method, path: url.pathname, query: url.search, headers }, null, 2) + '\n');
    console.log(name, req.method, url.pathname, headers['content-encoding'] || '-', body.length);

    res.writeHead(200, { 'content-type': 'application/json', ...cors });
    res.end('{}');
  });
}).listen(9911, '127.0.0.1');

http.createServer((req, res) => {
  res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
  res.end(fs.readFileSync(new URL('./browser/index.html', import.meta.url)));
}).listen(9912, '127.0.0.1');

console.log('capture: приём на :9911, страница браузера на :9912');

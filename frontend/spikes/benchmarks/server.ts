import { createReadStream, statSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { extname, join, normalize } from 'node:path';

const mime: Record<string, string> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.wasm': 'application/wasm',
};

export async function serveStatic(root: string): Promise<{ server: Server; url: string }> {
  const server = createServer((request, response) => {
    const pathname = new URL(request.url ?? '/', 'http://localhost').pathname;
    if (pathname === '/__axe.js') {
      response.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'");
      response.setHeader('Content-Type', 'text/javascript; charset=utf-8');
      createReadStream(join(process.cwd(), 'node_modules/axe-core/axe.min.js')).pipe(response);
      return;
    }
    const clean = normalize(pathname).replace(/^(\.\.[/\\])+/, '');
    let file = join(root, clean);
    try {
      const stats = statSync(file);
      if (stats.isDirectory()) file = join(file, 'index.html');
    } catch {
      file = join(root, clean, 'index.html');
    }
    response.setHeader('Content-Security-Policy', [
      "default-src 'self'",
      "script-src 'self'",
      "style-src 'self' 'unsafe-inline'",
      "img-src 'self' data:",
      "connect-src 'self'",
      "worker-src 'self'",
      "object-src 'none'",
      "base-uri 'none'",
      "frame-ancestors 'none'",
    ].join('; '));
    response.setHeader('Content-Type', mime[extname(file)] ?? 'application/octet-stream');
    createReadStream(file)
      .on('error', () => {
        response.statusCode = 404;
        response.end('not found');
      })
      .pipe(response);
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('server did not bind TCP');
  return { server, url: `http://127.0.0.1:${address.port}` };
}

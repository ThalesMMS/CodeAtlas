import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

const root = new URL('.', import.meta.url).pathname;
const output = join(root, 'generated');

const targets = [
  { name: '100kib', bytes: 100 * 1024 },
  { name: '1mib', bytes: 1024 * 1024 },
  { name: 'limit-1536kib', bytes: 1536 * 1024 },
];

const languages = [
  { ext: 'go', line: (i: number) => `func Synthetic${i}() int { return ${i} }\n` },
  { ext: 'js', line: (i: number) => `export function synthetic${i}() { return ${i}; }\n` },
  { ext: 'ts', line: (i: number) => `export function synthetic${i}(value: number): number { return value + ${i}; }\n` },
  { ext: 'tsx', line: (i: number) => `export function Synthetic${i}() { return <span>${i}</span>; }\n` },
];

function header(ext: string): string {
  if (ext === 'go') return 'package synthetic\n\n';
  return '/* synthetic CodeAtlas editor spike fixture */\n';
}

function build(ext: string, bytes: number, line: (i: number) => string): string {
  let content = header(ext);
  content += '// Unicode BMP: résumé Ω Ж | non-BMP: 🚀 𐍈\n';
  content += `// Long line: ${'x'.repeat(10_240)}\n`;
  let i = 0;
  while (Buffer.byteLength(content, 'utf8') < bytes) {
    const next = line(i);
    content += i % 17 === 0 ? next.replaceAll('\n', '\r\n') : next;
    i += 1;
  }
  return content.slice(0, bytes);
}

await mkdir(output, { recursive: true });
for (const target of targets) {
  for (const language of languages) {
    const path = join(output, `${target.name}.${language.ext}`);
    await writeFile(path, build(language.ext, target.bytes, language.line));
  }
}

console.log(`generated ${targets.length * languages.length} fixtures in ${output}`);

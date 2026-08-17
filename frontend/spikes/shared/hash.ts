export async function sha256(value: string): Promise<string> {
  const data = new TextEncoder().encode(value);
  const digest = await crypto.subtle.digest('SHA-256', data);
  return `sha256:${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('')}`;
}

export function lineColumnToOffset(content: string, line: number, column: number): number {
  let currentLine = 1;
  let currentColumn = 1;
  for (let offset = 0; offset < content.length; offset += 1) {
    if (currentLine === line && currentColumn === column) return offset;
    if (content[offset] === '\n') {
      currentLine += 1;
      currentColumn = 1;
    } else {
      currentColumn += 1;
    }
  }
  return content.length;
}

export function offsetToLineColumn(content: string, offset: number): { line: number; column: number } {
  let line = 1;
  let column = 1;
  const limit = Math.max(0, Math.min(offset, content.length));
  for (let index = 0; index < limit; index += 1) {
    if (content[index] === '\n') {
      line += 1;
      column = 1;
    } else {
      column += 1;
    }
  }
  return { line, column };
}

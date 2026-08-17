export function languageForPath(path: string, declaredLanguage = ''): string {
  const extension = path.toLowerCase().match(/\.([^.\/]+)$/)?.[1] ?? '';
  if (extension === 'go') return 'go';
  if (extension === 'js' || extension === 'mjs' || extension === 'cjs' || extension === 'jsx') return 'javascript';
  if (extension === 'ts' || extension === 'mts' || extension === 'cts' || extension === 'tsx') return 'typescript';
  if (extension === 'swift') return 'swift';
  if (extension === 'py') return 'python';
  if (extension === 'rs') return 'rust';
  const normalized = declaredLanguage.toLowerCase();
  if (normalized === 'jsx' || normalized === 'javascript') return 'javascript';
  if (normalized === 'tsx' || normalized === 'typescript') return 'typescript';
  if (normalized === 'py' || normalized === 'python') return 'python';
  if (normalized === 'rs' || normalized === 'rust') return 'rust';
  return normalized || 'plaintext';
}

export function workspaceModelURI(path: string): string {
  const normalized = path.replaceAll('\\', '/');
  const segments = normalized.split('/').filter((part) => part && part !== '.');
  if (normalized.startsWith('/') || /^[a-z]:\//i.test(normalized) || segments.includes('..')) {
    throw new Error('Monaco model path must be workspace-relative');
  }
  return `codeatlas://workspace/${segments.map(encodeURIComponent).join('/')}`;
}

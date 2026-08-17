import { resolve } from 'node:path';
import { defineConfig } from 'vite';

export default defineConfig({
  root: '.',
  base: './',
  server: {
    host: '127.0.0.1',
    strictPort: false,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    manifest: true,
    rollupOptions: {
      input: {
        monaco: resolve(__dirname, 'editor-monaco/index.html'),
        codemirror: resolve(__dirname, 'editor-codemirror/index.html'),
      },
    },
  },
});

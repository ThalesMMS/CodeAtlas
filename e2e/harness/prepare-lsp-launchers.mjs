import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { prepareFakeLSPLaunchers } from './lsp-launchers.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
await prepareFakeLSPLaunchers({ root, force: true });

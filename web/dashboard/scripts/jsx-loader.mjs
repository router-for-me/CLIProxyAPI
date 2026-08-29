// Custom Node.js ESM loader: transform .js / .jsx via esbuild so node --test
// can run JSX test files inside the dashboard's ESM project (type: "module").
//
// We intentionally do NOT cover .ts / .tsx — those have no callers under
// web/dashboard/src today. esbuild-register's own loader.js only matches
// .ts/.tsx/.mts/.cts, so a sibling .jsx-aware loader is needed.

import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { transformSync } from 'esbuild';

const JSX_EXTENSIONS = /\.jsx$/;

export async function load(url, context, defaultLoad) {
  if (!JSX_EXTENSIONS.test(url)) {
    return defaultLoad(url, context, defaultLoad);
  }

  const filepath = fileURLToPath(url);
  const source = await readFile(filepath, 'utf8');
  const { code } = transformSync(source, {
    sourcefile: filepath,
    loader: 'jsx',
    format: 'esm',
    target: 'node20',
    jsx: 'automatic',
    sourcemap: 'inline',
  });

  return {
    format: 'module',
    source: code,
    shortCircuit: true,
  };
}
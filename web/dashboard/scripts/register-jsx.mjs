// Bootstraps the JSX ESM loader before node --test runs.
//
// esbuild-register's own loader (esbuild-register/loader) only matches
// .ts/.tsx/.mts/.cts, so .jsx files in this ESM project (type: "module")
// still throw ERR_UNKNOWN_FILE_EXTENSION. We register a sibling loader that
// runs esbuild on .jsx with jsx: "automatic" so React 18's automatic JSX
// runtime is used.

import { register } from 'node:module';

register('./jsx-loader.mjs', import.meta.url);
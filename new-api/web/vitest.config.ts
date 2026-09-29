/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vitest/config'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

export default defineConfig({
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    environment: 'jsdom',
    server: {
      deps: { inline: [/@lobehub\//, /antd-style/] },
    },
    setupFiles: ['./src/test-setup.ts'],
    // Several heavy jsdom suites (channel-configuration, visual-billing-editor)
    // legitimately take >5s per test on contended CI runners; the vitest
    // default of 5000ms fails whichever of them crosses the line first. The
    // heaviest test measures ~3.2s uncontended, so 20s keeps headroom for the
    // ~4x slowdown observed on shared runners.
    testTimeout: 20000,
    clearMocks: true,
    restoreMocks: true,
    include: [
      'src/**/*.{test,spec}.{ts,tsx}',
      'scripts/oxlint/__tests__/*.test.ts',
    ],
  },
})

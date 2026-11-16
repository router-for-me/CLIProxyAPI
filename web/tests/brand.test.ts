import { describe, expect, test } from 'bun:test';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { PROX_LOGO_DATA_URI } from '../src/assets/proxLogo';

const webRoot = new URL('../', import.meta.url).pathname;
const readSource = (path: string) => readFileSync(join(webRoot, path), 'utf8');

const sourceFiles = readdirSync(join(webRoot, 'src'), { recursive: true, withFileTypes: true })
  .filter((entry) => entry.isFile())
  .map((entry) => join(entry.parentPath, entry.name));

describe('prox brand', () => {
  test('the logo is an inline SVG data URI', () => {
    expect(PROX_LOGO_DATA_URI.startsWith('data:image/svg+xml,')).toBe(true);
  });

  test('index.html is titled prox, carries the logo as favicon and sets the theme before paint', () => {
    const html = readSource('index.html');
    expect(html).toContain('<title>prox</title>');
    expect(html).toContain(PROX_LOGO_DATA_URI);
    expect(html).toContain('cli-proxy-theme');
    expect(html).not.toContain('CLI Proxy API Management Center');
  });

  test('main.tsx no longer overrides the favicon', () => {
    const main = readSource('src/main.tsx');
    expect(main).not.toContain('image/jpeg');
    expect(main).not.toContain('logoInline');
  });

  test('the JPEG logo is gone', () => {
    expect(existsSync(join(webRoot, 'src/assets/logoInline.ts'))).toBe(false);
    expect(existsSync(join(webRoot, 'logo.jpg'))).toBe(false);
    expect(existsSync(join(webRoot, 'logo.svg'))).toBe(true);
    const offenders = sourceFiles.filter((file) =>
      readFileSync(file, 'utf8').includes('INLINE_LOGO_JPEG')
    );
    expect(offenders).toEqual([]);
  });

  test('the sidebar brand row has no subtitle and no warm theme swatches', () => {
    const layout = readSource('src/components/layout/MainLayout.tsx');
    expect(layout).not.toContain('sidebar-brand-subtitle');
    expect(layout).not.toContain('#151412');
    expect(layout).not.toContain('#1d1b18');
  });
});

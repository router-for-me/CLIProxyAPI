import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const source = readFileSync('src/features/quota/QuotaPage.tsx', 'utf8');
const styles = readFileSync('src/features/quota/QuotaPage.module.scss', 'utf8');

describe('quota toolbar presentation contracts', () => {
  test('uses a named, explicit clear action and restores input focus', () => {
    expect(source).toContain('type="search"');
    expect(source).toContain('ref={searchInputRef}');
    expect(source).toContain('{search && (');
    expect(source).toContain("aria-label={t('quota_management.search_clear')}");
    expect(source).toContain(
      "handleSearchChange('');\n                  searchInputRef.current?.focus();"
    );
    expect(source).toContain('<IconX size={14} aria-hidden="true" />');
    expect(styles).toMatch(/&::-webkit-search-cancel-button,[\s\S]*?appearance: none;/);
  });

  test('groups search and sorting separately from provider navigation', () => {
    const toolbarStart = source.indexOf('<div className={styles.toolbar}>');
    const searchStart = source.indexOf('<div className={styles.search}>');
    const sortStart = source.indexOf('<div className={styles.sort}>');
    expect(toolbarStart).toBeGreaterThan(source.indexOf('<ProviderTabs'));
    expect(searchStart).toBeGreaterThan(toolbarStart);
    expect(sortStart).toBeGreaterThan(searchStart);
    expect(styles).toMatch(/\.toolbar\s*\{[^}]*flex-wrap: wrap;/);
    expect(styles).toContain('&:focus-within');
    expect(styles).toContain('&:focus-visible');
    expect(styles).toContain('@media (prefers-reduced-motion: reduce)');
  });
});

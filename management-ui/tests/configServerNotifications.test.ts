import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { hasTrustedProxiesChange } from '@/features/config/hooks/useConfigDocument';
import { findConfigFieldById } from '@/features/config/searchIndex';

const trusted = 'server: {trusted-proxies: [127.0.0.1, "2001:db8::/32"]}';

describe('trusted proxy restart notification', () => {
  test('detects adding, removing, and replacing trusted networks', () => {
    expect(hasTrustedProxiesChange('{}', trusted)).toBe(true);
    expect(hasTrustedProxiesChange(trusted, '{}')).toBe(true);
    expect(hasTrustedProxiesChange(trusted, 'server: {trusted-proxies: []}')).toBe(true);
    expect(hasTrustedProxiesChange(trusted, trusted.replace('127.0.0.1', '10.0.0.0/8'))).toBe(true);
  });

  test('ignores presentation changes, order, duplicates, and discovery-only edits', () => {
    expect(hasTrustedProxiesChange('{}', 'server: {trusted-proxies: []}')).toBe(false);
    expect(hasTrustedProxiesChange('server: null', 'server: {trusted-proxies: null}')).toBe(false);
    expect(hasTrustedProxiesChange(trusted, '# retained\n' + trusted)).toBe(false);
    expect(
      hasTrustedProxiesChange(
        trusted,
        'server: {trusted-proxies: ["2001:db8::/32", 127.0.0.1, 127.0.0.1]}'
      )
    ).toBe(false);
    expect(
      hasTrustedProxiesChange(
        trusted,
        trusted.replace('server: {', 'server: {discovery: {enabled: true}, ')
      )
    ).toBe(false);
    expect(hasTrustedProxiesChange('{}', 'server: {discovery: {enabled: true}}')).toBe(false);
  });

  test('supports YAML aliases and tolerates invalid documents during recovery', () => {
    expect(
      hasTrustedProxiesChange(
        '{}',
        'access: {api-keys: &fixture [127.0.0.1]}\nserver: {trusted-proxies: *fixture}'
      )
    ).toBe(true);
    expect(hasTrustedProxiesChange('server: [', '{}')).toBe(false);
  });

  test('warns on both successful save and readback after a partial visual write', () => {
    const source = readFileSync('src/features/config/hooks/useConfigDocument.ts', 'utf8');
    expect(source).toContain('hasTrustedProxiesChange(latestServerYaml, mergedYaml)');
    expect(source).toContain('hasTrustedProxiesChange(previewServerYaml, latestYaml)');
    expect(source.match(/notification\.trusted_proxies_restart_required/g)?.length).toBe(2);
  });
});

describe('server configuration localization', () => {
  for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
    test(`${locale} has labels, hints, validation and restart text`, async () => {
      const messages: unknown = await Bun.file(`src/i18n/locales/${locale}.json`).json();
      const resolve = (key: string) =>
        key.split('.').reduce<unknown>((node, part) => {
          if (node && typeof node === 'object') return (node as Record<string, unknown>)[part];
          return undefined;
        }, messages);
      for (const id of [
        'trustedProxies',
        'discoveryEnabled',
        'discoveryServiceName',
        'discoveryServiceType',
        'discoverySubtypes',
        'discoveryInterfacesInclude',
        'discoveryInterfacesExclude',
        'discoveryAuthRequired',
        'discoveryAdvertiseManagement',
      ]) {
        const entry = findConfigFieldById(id);
        expect(entry?.sectionId).toBe('connectivity');
        expect(entry?.labelKey).toBe(`config_management.visual.serverExtras.${id}.label`);
        expect(entry?.hintKey).toBe(`config_management.visual.serverExtras.${id}.hint`);
        expect(resolve(entry!.labelKey)).toBeString();
        expect(resolve(entry!.hintKey!)).toBeString();
      }
      for (const key of [
        'notification.trusted_proxies_restart_required',
        'config_management.visual.serverExtras.discoveryTitle',
        'config_management.visual.serverExtras.discoveryHint',
        'config_management.visual.validation.invalid_trusted_proxies',
        'config_management.visual.validation.invalid_discovery_service_type',
      ])
        expect(resolve(key)).toBeString();
    });
  }
});

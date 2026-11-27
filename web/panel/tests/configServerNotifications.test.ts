import { describe, expect, test } from 'bun:test';
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
});

describe('server configuration search metadata', () => {
  test.each([
    'trustedProxies',
    'discoveryEnabled',
    'discoveryServiceName',
    'discoveryServiceType',
    'discoverySubtypes',
    'discoveryInterfacesInclude',
    'discoveryInterfacesExclude',
    'discoveryAuthRequired',
    'discoveryAdvertiseManagement',
  ])('%s is a connectivity field with serverExtras copy', (id) => {
    const entry = findConfigFieldById(id);
    expect(entry?.sectionId).toBe('connectivity');
    expect(entry?.labelKey).toBe(`config_management.visual.serverExtras.${id}.label`);
    // The service type's default is its placeholder; it has no separate explanation.
    expect(entry?.hintKey).toBe(
      id === 'discoveryServiceType' ? undefined : `config_management.visual.serverExtras.${id}.hint`
    );
  });
});

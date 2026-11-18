import { describe, expect, test } from 'bun:test';
import { parseDocument } from 'yaml';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { useVisualConfig, getVisualConfigValidationErrors } from '../src/hooks/useVisualConfig';
import { DEFAULT_VISUAL_VALUES, type VisualConfigValues } from '../src/types/visualConfig';
import {
  ADDITION_FIELDS,
  ICE_KEY,
  ICE_PATH,
  goDurationSeconds,
} from '@/features/config/visualConfigAdditions';
import { ConfigDraftConflictError } from '../src/services/api/configPatch';
import { runVisualConfig } from './helpers/visualConfig';

const errors = (patch: Partial<VisualConfigValues>) =>
  getVisualConfigValidationErrors({
    ...structuredClone(DEFAULT_VISUAL_VALUES),
    codexLiveMediaRelayEnabled: true,
    ...patch,
  });
const iceYaml = `# root
oauth:
  providers:
    codex:
      live-media-relay:
        ice-servers:
          # row-a
          - urls: [stun:a.example]
            username: user-a
            credential: fixture-a # secret-a
            future: a
          # row-b
          - urls: [turn:b.example]
            username: user-b
            credential: fixture-b # secret-b
            future: b
api-keys: {codex: [{future: untouched}]}
`;

describe('visual config additions', () => {
  for (const { key, path, kind } of ADDITION_FIELDS) {
    test(`${key}: reads/writes exact v8 path and preserves unknown fields`, () => {
      const doc = parseDocument(
        'api-keys: {codex: [{future: untouched}]}\n# retained\nfuture: true\n'
      );
      const originalValue = kind === 'boolean' ? true : kind === 'integer' ? 0 : 'original';
      doc.setIn(path, originalValue);
      const yaml = doc.toString();
      const loaded = runVisualConfig(yaml);
      expect(loaded.visualValues[key]).toBe(kind === 'boolean' ? true : String(originalValue));
      expect(loaded.visualDirty).toBe(false);
      const value = kind === 'boolean' ? false : kind === 'integer' ? '-1' : 'changed';
      const config = runVisualConfig(yaml, [{ [key]: value }]);
      const output = config.applyVisualChangesToYaml(yaml);
      expect(parseDocument(output).getIn(path)).toBe(kind === 'integer' ? -1 : value);
      expect(parseDocument(output).toJS()['api-keys']).toEqual(doc.toJS()['api-keys']);
      expect(output).toContain('# retained');
      expect([...config.visualDirtyFields]).toEqual([key]);
      if (kind === 'boolean') {
        const falseDoc = parseDocument(output);
        expect(runVisualConfig(falseDoc.toString()).visualValues[key]).toBe(false);
      }
    });
  }
  test('missing defaults, explicit false override, null parents, negative cooling and optional blanks', () => {
    const config = runVisualConfig('routing: null\n', [
      { routingSessionAffinitySubagents: false, transientErrorCooldownSeconds: '-10' },
    ]);
    const out = parseDocument(config.applyVisualChangesToYaml('routing: null\n'));
    expect(out.getIn(['routing', 'session-affinity-subagents'])).toBe(false);
    expect(out.getIn(['routing', 'cooldown', 'transient-error-cooldown-seconds'])).toBe(-10);
    expect(config.visualValidationErrors.transientErrorCooldownSeconds).toBeUndefined();
    const defaults = runVisualConfig('{}').visualValues;
    for (const { key } of ADDITION_FIELDS) expect(defaults[key]).toBe(DEFAULT_VISUAL_VALUES[key]);
    expect(Object.values(errors({})).filter(Boolean)).toEqual([]);
    const cleared = runVisualConfig('multimedia: {video-result-auth-cache-ttl: 3h, future: 1}', [
      { videoResultAuthCacheTTL: '' },
    ]);
    expect(
      parseDocument(
        cleared.applyVisualChangesToYaml('multimedia: {video-result-auth-cache-ttl: 3h, future: 1}')
      ).getIn(['multimedia', 'video-result-auth-cache-ttl'])
    ).toBeUndefined();
  });
  test('ICE reorder/edit/delete preserve secrets, unknown keys, AST comments, and unrelated concurrent edits', () => {
    const rows = runVisualConfig(iceYaml).visualValues[ICE_KEY];
    const desired = [{ ...rows[1], username: '' }, rows[0]];
    const config = runVisualConfig(iceYaml, [{ [ICE_KEY]: desired }]);
    const out = config.applyVisualChangesToYaml(
      iceYaml.replace('future: untouched', 'future: concurrent')
    );
    const saved = parseDocument(out).getIn(ICE_PATH)?.toJSON();
    expect(saved.map((row: { future: string }) => row.future)).toEqual(['b', 'a']);
    expect(saved[0]).toMatchObject({ username: '', credential: 'fixture-b' });
    expect(out).toContain('# secret-b');
    expect(out).toContain('# row-b');
    expect(out).toContain('future: concurrent');
    const removed = runVisualConfig(iceYaml, [{ [ICE_KEY]: [rows[1]] }]).applyVisualChangesToYaml(
      iceYaml
    );
    expect(removed).not.toContain('fixture-a');
    expect(removed).not.toContain('# row-a');
    expect(removed).toContain('# secret-b');
    const cleared = runVisualConfig(iceYaml, [{ [ICE_KEY]: [] }]).applyVisualChangesToYaml(iceYaml);
    expect(parseDocument(cleared).getIn(ICE_PATH)?.toJSON()).toEqual([]);
    expect(cleared).not.toContain('fixture-b');
    expect(() =>
      config.applyVisualChangesToYaml(iceYaml.replace('future: b', 'future: concurrent'))
    ).toThrow(ConfigDraftConflictError);
    expect(() =>
      config.applyVisualChangesToYaml(iceYaml.replace('fixture-b', 'concurrent-secret'))
    ).toThrow(ConfigDraftConflictError);
  });
  test('ICE IDs alone are not dirty; adding a row writes secrets only to OAuth', () => {
    const rows = runVisualConfig(iceYaml).visualValues[ICE_KEY];
    expect(
      runVisualConfig(iceYaml, [
        { [ICE_KEY]: rows.map((row) => ({ ...row, id: 'new-' + row.id })) },
      ]).visualDirty
    ).toBe(false);
    const row = {
      id: 'new',
      urlsText: 'stun:a.example\n turn:b.example?transport=tcp ',
      username: 'u',
      credential: 'test-secret',
    };
    const config = runVisualConfig('{}', [{ [ICE_KEY]: [row] }]);
    expect(parseDocument(config.applyVisualChangesToYaml('{}')).getIn(ICE_PATH)?.toJSON()).toEqual([
      {
        urls: ['stun:a.example', 'turn:b.example?transport=tcp'],
        username: 'u',
        credential: 'test-secret',
      },
    ]);
  });
  test('ICE rebase retains draft lineage and guards subsequent concurrent list changes; load resets session', () => {
    let phaseResult = false;
    function Harness() {
      const v = useVisualConfig();
      const [phase, next] = useState(0);
      if (phase === 0) v.loadVisualValuesFromYaml(iceYaml);
      if (phase === 1)
        v.setVisualValues({
          [ICE_KEY]: [{ ...v.visualValues[ICE_KEY][1], urlsText: 'turn:renamed.example' }],
        });
      if (phase === 2) v.rebaseVisualValuesFromYaml(iceYaml, v.applyVisualChangesToYaml(iceYaml));
      if (phase === 3) {
        const out = v.applyVisualChangesToYaml(iceYaml);
        expect(out).toContain('fixture-b');
        expect(out).not.toContain('fixture-a');
        expect(out).toContain('future: b');
        expect(() =>
          v.applyVisualChangesToYaml(iceYaml.replace('future: b', 'future: changed'))
        ).toThrow(ConfigDraftConflictError);
        v.loadVisualValuesFromYaml('{}');
      }
      if (phase === 4) {
        expect(v.visualValues[ICE_KEY]).toEqual([]);
        expect(v.visualDirty).toBe(false);
        expect(v.applyVisualChangesToYaml('{}')).not.toContain('fixture-b');
        phaseResult = true;
      } else next(phase + 1);
      return null;
    }
    renderToStaticMarkup(createElement(Harness));
    expect(phaseResult).toBe(true);
  });
});

describe('addition validation boundaries', () => {
  test('Go durations, limits and bootstrap aliases', () => {
    for (const value of ['1ns', '1us', '1µs', '1μs', '.5s', '+1h2m3.5s', '210s', '211s']) {
      expect(errors({ videoResultAuthCacheTTL: value }).videoResultAuthCacheTTL).toBeUndefined();
      expect(
        errors({ antigravityConnectionPoolIdleTimeout: value }).antigravityConnectionPoolIdleTimeout
      ).toBeUndefined();
    }
    for (const value of ['1d', '1e3s', '1 s', 's', '9223372036854775808ns'])
      expect(errors({ videoResultAuthCacheTTL: value }).videoResultAuthCacheTTL).toBe(
        'invalid_duration'
      );
    for (const value of ['0', '-1s', '0.1ns'])
      expect(errors({ videoResultAuthCacheTTL: value }).videoResultAuthCacheTTL).toBe(
        'positive_duration'
      );
    expect(goDurationSeconds('9223372036854775807ns')).toBeDefined();
    for (const value of [
      '0',
      '0s',
      'none',
      'UNLIMITED',
      'disabled',
      'off',
      'never',
      '15',
      '+15',
      '9223372036',
    ])
      expect(
        errors({ codexStreamBootstrapTimeout: value }).codexStreamBootstrapTimeout
      ).toBeUndefined();
    for (const value of ['-1', '-1s', '9223372037', '15.5', '1d'])
      expect(errors({ codexStreamBootstrapTimeout: value }).codexStreamBootstrapTimeout).toBe(
        'invalid_duration'
      );
  });
  test('signed cooldown and nonnegative counts', () => {
    expect(
      errors({ transientErrorCooldownSeconds: '-1' }).transientErrorCooldownSeconds
    ).toBeUndefined();
    expect(errors({ transientErrorCooldownSeconds: '1.5' }).transientErrorCooldownSeconds).toBe(
      'integer'
    );
    for (const value of ['0', '100', ''])
      expect(
        errors({ codexLiveMediaRelayMaxSessions: value }).codexLiveMediaRelayMaxSessions
      ).toBeUndefined();
    for (const value of ['-1', '1.5', '9007199254740992'])
      expect(errors({ codexLiveMediaRelayMaxSessions: value }).codexLiveMediaRelayMaxSessions).toBe(
        'non_negative_integer'
      );
  });
  test('Antigravity short-connection overrides remain valid (executor pool contract)', () => {
    for (const value of ['-1', '0', '2', '100', '101', ''])
      expect(
        errors({ antigravityConnectionPoolMaxIdleConnsPerHost: value })
          .antigravityConnectionPoolMaxIdleConnsPerHost
      ).toBeUndefined();
    for (const value of ['1.5', '9007199254740992'])
      expect(
        errors({ antigravityConnectionPoolMaxIdleConnsPerHost: value })
          .antigravityConnectionPoolMaxIdleConnsPerHost
      ).toBe('integer');
    for (const value of ['0', '-1s', '-1h', '30s', '210s', '211s', ''])
      expect(
        errors({ antigravityConnectionPoolIdleTimeout: value }).antigravityConnectionPoolIdleTimeout
      ).toBeUndefined();
  });
  test('timezone and literal IPv4/IPv6 (not URLs, DNS, zones or numeric aliases)', () => {
    for (const value of ['UTC', 'Local', 'Asia/Singapore', 'America/New_York', ''])
      expect(errors({ claudeHeaderTimezone: value }).claudeHeaderTimezone).toBeUndefined();
    for (const value of ['bad/zone', '+01:00'])
      expect(errors({ claudeHeaderTimezone: value }).claudeHeaderTimezone).toBe('invalid_timezone');
    for (const value of ['', '127.0.0.1', '203.0.113.1', '::1', '2001:db8::1', '::ffff:192.0.2.1'])
      expect(
        errors({ codexLiveMediaRelayPublicIP: value }).codexLiveMediaRelayPublicIP
      ).toBeUndefined();
    for (const value of [
      'localhost',
      'https://1.2.3.4',
      '123',
      '256.1.1.1',
      '01.2.3.4',
      '[::1]',
      'fe80::1%eth0',
      '1::2::3',
    ])
      expect(errors({ codexLiveMediaRelayPublicIP: value }).codexLiveMediaRelayPublicIP).toBe(
        'invalid_ip'
      );
  });
  test('disabled relay permits dormant settings but retains YAML type constraints', () => {
    const dormant: Partial<VisualConfigValues> = {
      codexLiveMediaRelayEnabled: false,
      codexLiveMediaRelayMaxSessions: '-1',
      codexLiveMediaRelayPublicIP: 'not-yet-configured',
      codexLiveMediaRelayUDPPortMin: '40000',
      codexLiveMediaRelayUDPPortMax: '',
      codexLiveMediaRelayICEServers: [{ id: 'draft', urlsText: '', username: '', credential: '' }],
    };
    expect(Object.values(errors(dormant)).filter(Boolean)).toEqual([]);
    expect(
      errors({ ...dormant, codexLiveMediaRelayUDPPortMin: '65536' }).codexLiveMediaRelayUDPPortMin
    ).toBe('integer_range_0_65535');
    expect(
      errors({ ...dormant, codexLiveMediaRelayMaxSessions: '1.5' }).codexLiveMediaRelayMaxSessions
    ).toBe('integer');
    const enabled = errors({ ...dormant, codexLiveMediaRelayEnabled: true });
    expect(enabled.codexLiveMediaRelayMaxSessions).toBe('non_negative_integer');
    expect(enabled.codexLiveMediaRelayPublicIP).toBe('invalid_ip');
    expect(enabled.codexLiveMediaRelayUDPPortMax).toBe('udp_port_pair');
    expect(enabled.codexLiveMediaRelayICEServers).toBe('invalid_ice_servers');
    const config = runVisualConfig(
      'oauth: {providers: {claude: {header-defaults: {timezone: Local}}, codex: {live-media-relay: {enabled: false, public-ip: pending, udp-port-min: 40000}}}}',
      [{ saveCooldownStatus: true }]
    );
    expect(Object.values(config.visualValidationErrors).filter(Boolean)).toEqual([]);
  });
  test('UDP pairs, default 32-session capacity, numeric boundaries and optional zero', () => {
    expect(
      errors({ codexLiveMediaRelayUDPPortMin: '0', codexLiveMediaRelayUDPPortMax: '0' })
        .codexLiveMediaRelayUDPPortMin
    ).toBeUndefined();
    for (const value of ['65536', '-1', '1.5'])
      expect(errors({ codexLiveMediaRelayUDPPortMin: value }).codexLiveMediaRelayUDPPortMin).toBe(
        'integer_range_0_65535'
      );
    expect(errors({ codexLiveMediaRelayUDPPortMin: '1' }).codexLiveMediaRelayUDPPortMax).toBe(
      'udp_port_pair'
    );
    expect(
      errors({ codexLiveMediaRelayUDPPortMin: '100', codexLiveMediaRelayUDPPortMax: '99' })
        .codexLiveMediaRelayUDPPortMin
    ).toBe('udp_port_pair');
    expect(
      errors({ codexLiveMediaRelayUDPPortMin: '100', codexLiveMediaRelayUDPPortMax: '162' })
        .codexLiveMediaRelayUDPPortMax
    ).toBe('udp_port_capacity');
    expect(
      errors({ codexLiveMediaRelayUDPPortMin: '100', codexLiveMediaRelayUDPPortMax: '163' })
        .codexLiveMediaRelayUDPPortMax
    ).toBeUndefined();
    expect(
      errors({
        codexLiveMediaRelayUDPPortMin: '65534',
        codexLiveMediaRelayUDPPortMax: '65535',
        codexLiveMediaRelayMaxSessions: '1',
      }).codexLiveMediaRelayUDPPortMax
    ).toBeUndefined();
  });
  test('ICE optional list, required row URLs and allowed URL schemes', () => {
    for (const urlsText of [
      'stun:a.example\nturn:b.example?transport=udp',
      'stuns:a.example',
      'turns:b.example',
    ])
      expect(
        errors({ [ICE_KEY]: [{ id: 'a', urlsText, username: '', credential: '' }] })[ICE_KEY]
      ).toBeUndefined();
    for (const urlsText of ['', 'https://example.com', 'turn:bad%escape', 'turn:bad host'])
      expect(
        errors({ [ICE_KEY]: [{ id: 'a', urlsText, username: '', credential: '' }] })[ICE_KEY]
      ).toBe('invalid_ice_servers');
  });
});

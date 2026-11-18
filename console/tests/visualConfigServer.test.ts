import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parseDocument } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';
import { DEFAULT_VISUAL_VALUES } from '../src/types/visualConfig';
import {
  SERVER_FIELDS,
  validateVisualServer,
  validTrustedProxy,
} from '../src/features/config/visualConfigServer';
import { ConfigDraftConflictError } from '../src/services/api/configPatch';
import { runVisualConfig } from './helpers/visualConfig';

const yaml = `# root
server:
  trusted-proxies: [127.0.0.1] # trusted
  discovery:
    enabled: true
    service-type: _ai-gateway._tcp # type
    subtypes: [responses, responses] # subtype
    interfaces:
      include: [en0]
      exclude: [utun*]
      future: retained # interface
    future: retained # discovery
  future: retained # server
api-keys: {codex: [{future: untouched}]}
`;

describe('server visual codec', () => {
  for (const { key, path, kind } of SERVER_FIELDS) {
    test(`${key}: exact read/write path, explicit values, source roundtrip`, () => {
      const doc = parseDocument(yaml);
      const before = kind === 'list' ? ['one', 'one'] : kind === 'boolean' ? true : 'original';
      doc.setIn(path, before);
      const source = doc.toString();
      const loaded = runVisualConfig(source);
      expect(loaded.visualValues[key]).toEqual(before);
      const desired =
        kind === 'list' ? [' two ', '', 'two'] : kind === 'boolean' ? false : 'changed';
      const config = runVisualConfig(source, [{ [key]: desired }]);
      expect([...config.visualDirtyFields]).toEqual([key]);
      const output = config.applyVisualChangesToYaml(source);
      expect(parseDocument(output).getIn(path, true)?.toJSON()).toEqual(
        kind === 'list' ? ['two', 'two'] : desired
      );
      expect(runVisualConfig(output).visualValues[key]).toEqual(
        kind === 'list' ? ['two', 'two'] : desired
      );
      expect(output).toContain('# root');
      expect(output).toContain('# interface');
      expect(parseDocument(output).toJS()['api-keys']).toEqual(doc.toJS()['api-keys']);
      if (kind === 'list') {
        const clear = runVisualConfig(source, [{ [key]: [] }]).applyVisualChangesToYaml(source);
        expect(parseDocument(clear).getIn(path, true)?.toJSON()).toEqual([]);
        expect(clear).toContain('[]');
        expect(runVisualConfig(source, [{ [key]: [...(before as string[])] }]).visualDirty).toBe(
          false
        );
      }
    });
  }
  test('defaults and untouched values are not materialized or normalized', () => {
    for (const source of ['{}', 'server: null', 'server: {discovery: null}']) {
      const v = runVisualConfig(source);
      for (const { key } of SERVER_FIELDS)
        expect(v.visualValues[key]).toEqual(DEFAULT_VISUAL_VALUES[key]);
      expect(parseDocument(v.applyVisualChangesToYaml(source)).toJS()).toEqual(
        parseDocument(source).toJS()
      );
    }
    const source = yaml.replace(
      'subtypes: [responses, responses]',
      'subtypes: [" odd._sub ", "", 7, future]'
    );
    const v = runVisualConfig(source, [{ discoveryAuthRequired: false }]);
    const output = v.applyVisualChangesToYaml(source);
    expect(output).toContain('auth-required: false');
    expect(output).toContain('" odd._sub "');
    expect(output).toContain('# type');
    const original = parseDocument(source).toJS();
    original.server.discovery['auth-required'] = false;
    expect(parseDocument(output).toJS()).toEqual(original);
    expect(validateVisualServer(DEFAULT_VISUAL_VALUES)).toEqual({
      trustedProxies: undefined,
      discoveryServiceType: undefined,
    });
  });
  test('all null parent depths are safe and false overrides default true', () => {
    for (const source of [
      'server: null',
      'server: {discovery: null}',
      'server: {discovery: {interfaces: null}}',
    ]) {
      const v = runVisualConfig(source, [
        { discoveryInterfacesInclude: ['en*'], discoveryAuthRequired: false },
      ]);
      const output = parseDocument(v.applyVisualChangesToYaml(source));
      expect(output.getIn(['server', 'discovery', 'auth-required'])).toBe(false);
      expect(
        output.getIn(['server', 'discovery', 'interfaces', 'include'], true)?.toJSON()
      ).toEqual(['en*']);
    }
  });
  for (const { key, path, kind } of SERVER_FIELDS) {
    if (kind !== 'list') continue;
    test(`${key}: atomic concurrency guard and unrelated concurrent preservation`, () => {
      const config = runVisualConfig(yaml, [{ [key]: ['changed'] }]);
      const concurrent = parseDocument(yaml);
      concurrent.setIn(path, ['remote']);
      expect(() => config.applyVisualChangesToYaml(concurrent.toString())).toThrow(
        ConfigDraftConflictError
      );
      const output = config.applyVisualChangesToYaml(
        yaml.replace('future: untouched', 'future: concurrent')
      );
      expect(output).toContain('future: concurrent');
      expect(() => config.applyVisualChangesToYaml(output)).not.toThrow();
      expect(() => config.applyVisualChangesToYaml(concurrent.toString(), 'draft')).not.toThrow();
    });
  }
  test('rebase retains list comments, detects conflicts and load resets session state', () => {
    let done = false;
    function Harness() {
      const v = useVisualConfig();
      const [phase, next] = useState(0);
      if (phase === 0) v.loadVisualValuesFromYaml(yaml);
      if (phase === 1)
        v.setVisualValues({ discoverySubtypes: ['custom', 'responses'], trustedProxies: [] });
      if (phase === 2) v.rebaseVisualValuesFromYaml(yaml, v.applyVisualChangesToYaml(yaml));
      if (phase === 3) {
        const output = v.applyVisualChangesToYaml(yaml);
        expect(output).toContain('# subtype');
        expect(parseDocument(output).getIn(['server', 'trusted-proxies'], true)?.toJSON()).toEqual(
          []
        );
        expect(() =>
          v.applyVisualChangesToYaml(yaml.replace('[responses, responses]', '[remote]'))
        ).toThrow(ConfigDraftConflictError);
        v.setVisualValues({ discoverySubtypes: ['custom', 'responses', 'added'] });
      }
      if (phase === 4) {
        expect(v.applyVisualChangesToYaml(yaml)).toContain('added');
        v.loadVisualValuesFromYaml('server: {discovery: {service-name: new-session}}');
      }
      if (phase === 5) {
        expect(v.visualDirty).toBe(false);
        expect(v.visualValues.discoverySubtypes).toEqual([]);
        expect(v.visualValues.discoveryServiceName).toBe('new-session');
        expect(v.applyVisualChangesToYaml('{}')).not.toContain('custom');
        done = true;
      } else next(phase + 1);
      return null;
    }
    renderToStaticMarkup(createElement(Harness));
    expect(done).toBe(true);
  });
});

describe('backend server validation', () => {
  test('strict IPv4/IPv6 and CIDR boundaries; trims and removes blank lines', () => {
    for (const value of [
      '0.0.0.0/0',
      '255.255.255.255/32',
      '127.0.0.1',
      '::/0',
      '::1/128',
      '2001:db8::/64',
      '::ffff:192.0.2.1/128',
      '1:2:3:4:5:6:7:8',
      ' 192.0.2.0/24 ',
    ]) {
      expect(validTrustedProxy(value)).toBe(true);
    }
    for (const value of [
      'localhost',
      'https://127.0.0.1',
      '127.1',
      '012.0.0.1',
      '256.0.0.1',
      '1.2.3.4/33',
      '::/129',
      '1.2.3.4/-1',
      '::/+1',
      '::/1.5',
      '::/1e2',
      '::/',
      '::/1/2',
      'fe80::1%en0',
      '1.2. 3.4',
      '::ffff:192.000.2.1',
      '1:2:3:4:5:6:7',
      '1::2::3',
      '[::1]',
      ':: /64',
    ]) {
      expect(validTrustedProxy(value)).toBe(false);
      expect(
        validateVisualServer({ ...DEFAULT_VISUAL_VALUES, trustedProxies: [value] }).trustedProxies
      ).toBe('invalid_trusted_proxies');
    }
    expect(
      validateVisualServer({ ...DEFAULT_VISUAL_VALUES, trustedProxies: [' ', '\n', ' ::1 '] })
        .trustedProxies
    ).toBeUndefined();
  });
  test('enabled service type follows backend rules, including numeric names', () => {
    for (const value of ['', '  ', '_a._tcp', '_123._tcp', '_a-b._tcp', '_123456789012345._tcp']) {
      expect(
        validateVisualServer({
          ...DEFAULT_VISUAL_VALUES,
          discoveryEnabled: true,
          discoveryServiceType: value,
        }).discoveryServiceType
      ).toBeUndefined();
    }
    for (const value of [
      '_1234567890123456._tcp',
      '_-a._tcp',
      '_a-._tcp',
      '_a_b._tcp',
      'a._tcp',
      '_a._udp',
      '_a._TCP',
      '_é._tcp',
      '_._tcp',
    ]) {
      expect(
        validateVisualServer({
          ...DEFAULT_VISUAL_VALUES,
          discoveryEnabled: true,
          discoveryServiceType: value,
        }).discoveryServiceType
      ).toBe('invalid_discovery_service_type');
      expect(
        validateVisualServer({ ...DEFAULT_VISUAL_VALUES, discoveryServiceType: value })
          .discoveryServiceType
      ).toBeUndefined();
    }
  });
});

import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { parseDocument } from 'yaml';
import i18n from '@/i18n';
import { SectionConnectivity } from '@/features/config/components/sections/SectionConnectivity';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import { runVisualConfig } from './helpers/visualConfig';

const source = `# config
server:
  github-token: fixture-token # credential
  future: preserved
plugins:
  store-auth:
    - match: https://api.github.com/
      type: none
`;

describe('global GitHub token configuration', () => {
  test('loads, replaces and clears the canonical field without changing store auth', () => {
    expect(runVisualConfig(source).visualValues.githubToken).toBe('fixture-token');
    for (const token of [' replacement-fixture ', '']) {
      const config = runVisualConfig(source, [{ githubToken: token }]);
      expect([...config.visualDirtyFields]).toEqual(['githubToken']);
      const output = config.applyVisualChangesToYaml(source);
      const expected = parseDocument(source).toJS();
      expected.server['github-token'] = token.trim();
      expect(parseDocument(output).toJS()).toEqual(expected);
      expect(output).toContain('# credential');
      expect(runVisualConfig(output).visualValues.githubToken).toBe(token.trim());
    }
  });

  test('preserves untouched tokens and does not materialize an absent token', () => {
    const config = runVisualConfig(source, [{ port: '9000' }]);
    expect(
      parseDocument(config.applyVisualChangesToYaml(source)).getIn(['server', 'github-token'])
    ).toBe('fixture-token');
    for (const yaml of ['{}', 'server: null', 'server: {github-token: null}']) {
      const loaded = runVisualConfig(yaml);
      expect(loaded.visualValues.githubToken).toBe('');
      expect(parseDocument(loaded.applyVisualChangesToYaml(yaml)).toJS()).toEqual(
        parseDocument(yaml).toJS()
      );
      const edited = runVisualConfig(yaml, [{ githubToken: 'fixture-token' }]);
      expect(
        parseDocument(edited.applyVisualChangesToYaml(yaml)).getIn(['server', 'github-token'])
      ).toBe('fixture-token');
    }
  });

  test('renders a masked, labelled input with a hint and disabled support', () => {
    const translations = i18n.cloneInstance({ lng: 'en' });
    for (const disabled of [false, true]) {
      const markup = renderToStaticMarkup(
        createElement(
          I18nextProvider,
          { i18n: translations },
          createElement(SectionConnectivity, {
            values: DEFAULT_VISUAL_VALUES,
            disabled,
            onChange: () => {},
          })
        )
      );
      const field = markup
        .split('id="cfg-field-githubToken"')[1]
        .split('id="cfg-field-trustedProxies"')[0];
      const input = field.match(/<input\b[^>]*>/)?.[0] ?? '';
      expect(input).toContain('type="password"');
      expect(input).toContain('autoComplete="new-password"');
      expect(input.includes('disabled=""')).toBe(disabled);
      const id = input.match(/\bid="([^"]+)"/)?.[1];
      expect(id).toBeTruthy();
      expect(field).toContain(`for="${id}"`);
      expect(field).toContain(`id="${id}-hint"`);
      expect(input).toContain(`aria-describedby="${id}-hint"`);
    }
  });
});

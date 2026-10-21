import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import { SectionAdvanced } from '@/features/config/components/sections/SectionAdvanced';
import { SectionNetwork } from '@/features/config/components/sections/SectionNetwork';
import { SectionOAuthBehavior } from '@/features/config/components/sections/SectionOAuthBehavior';
import { CodexLiveICEServersEditor } from '@/features/config/components/blocks/CodexLiveICEServersEditor';
import { CONFIG_SECTION_IDS } from '@/features/config/constants';
import { CONFIG_FIELD_SEARCH_INDEX } from '@/features/config/searchIndex';

const translations = i18n.cloneInstance({ lng: 'en' });
const noop = () => {};
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n: translations }, element));
const props = { values: DEFAULT_VISUAL_VALUES, disabled: false, onChange: noop };
const escapeText = (value: string) =>
  renderToStaticMarkup(createElement('span', null, value)).slice(6, -7);
const text = (key: string) =>
  escapeText(translations.t(`config_management.visual.additions.${key}`));
const server = {
  id: 'fixture-ice',
  urlsText: 'stun:stun.example.test:3478\nturn:turn.example.test:3478',
  username: 'test-user',
  credential: 'synthetic-turn-password',
};

describe('OAuth behavior configuration UI', () => {
  test('keeps six canonical tabs and places every addition in its designated section', () => {
    expect(CONFIG_SECTION_IDS).toHaveLength(6);
    const network = render(createElement(SectionNetwork, props));
    const advanced = render(createElement(SectionAdvanced, props));
    const additions = CONFIG_FIELD_SEARCH_INDEX.filter((entry) =>
      entry.labelKey.includes('.additions.')
    );
    expect(additions).toHaveLength(26);
    for (const entry of additions) {
      const own = entry.sectionId === 'network' ? network : advanced;
      const other = entry.sectionId === 'network' ? advanced : network;
      expect(own).toContain(`id="cfg-field-${entry.fieldId}"`);
      expect(other).not.toContain(`id="cfg-field-${entry.fieldId}"`);
      expect(own).toContain(escapeText(translations.t(entry.labelKey)));
      expect(own).toContain(escapeText(translations.t(entry.hintKey!)));
      expect(entry.yamlKeys?.join('.')).toMatch(/^(routing|multimedia|oauth|upstream|client)\./);
    }
    const oauth = render(createElement(SectionOAuthBehavior, props));
    expect(oauth).toContain('<details');
    expect(oauth).toContain(text('oauthTitle'));
    expect(oauth).toContain(text('oauthHint'));
    expect(oauth).not.toContain('cfg-field-claudeHeaderTimezone');
    expect(oauth).toContain(text('liveRelayHint'));
  });

  test('renders labeled multi-line ICE URLs, masked credentials, and indexed removal actions', () => {
    const markup = render(
      createElement(CodexLiveICEServersEditor, {
        value: [server, { ...server, id: 'second-ice' }],
        onChange: noop,
      })
    );
    expect(markup.match(/<textarea\b/g)).toHaveLength(2);
    expect(markup).toContain(server.urlsText);
    const passwordInputs = markup.match(/<input\b[^>]*type="password"[^>]*>/g) ?? [];
    expect(passwordInputs).toHaveLength(2);
    for (const input of passwordInputs) {
      expect(input).toContain('autoComplete="new-password"');
      expect(input).toContain(`value="${server.credential}"`);
    }
    const inputs = markup.match(/<(?:input|textarea)\b[^>]*>/g) ?? [];
    expect(inputs).toHaveLength(6);
    for (const input of inputs) {
      const id = input.match(/\bid="([^"]+)"/)?.[1];
      expect(id).toBeTruthy();
      expect(markup).toContain(`for="${id}"`);
    }
    expect(markup).toContain(text('iceURLs'));
    expect(markup).toContain(text('iceUsername'));
    expect(markup).toContain(text('iceCredential'));
    expect(markup).toContain('aria-label="Remove ICE server 1"');
    expect(markup).toContain('aria-label="Remove ICE server 2"');
    expect(markup).not.toContain('{{index}}');
  });

  test('disables every editor and ICE action when configuration is read-only', () => {
    const values = { ...DEFAULT_VISUAL_VALUES, codexLiveMediaRelayICEServers: [server] };
    for (const Section of [SectionOAuthBehavior, SectionNetwork, SectionAdvanced]) {
      const markup = render(createElement(Section, { ...props, values, disabled: true }));
      const controls = markup.match(/<(?:input|textarea|button)\b[^>]*>/g) ?? [];
      expect(controls.length).toBeGreaterThan(0);
      for (const control of controls) expect(control).toContain('disabled=""');
    }
  });

  test('preserves blank string inputs and permits negative transient cooldowns', () => {
    const markup = render(createElement(SectionOAuthBehavior, props));
    for (const input of markup.match(/<input\b[^>]*type="(?:text|number)"[^>]*>/g) ?? []) {
      expect(input).toContain('value=""');
    }
    const network = render(
      createElement(SectionNetwork, {
        ...props,
        values: { ...DEFAULT_VISUAL_VALUES, transientErrorCooldownSeconds: '-1' },
      })
    );
    const input = network.match(/<input\b[^>]*value="-1"[^>]*>/)?.[0];
    expect(input).toContain('type="number"');
    expect(input).not.toContain('min=');
  });

  test('renders translated validation errors and associates the ICE error with its controls', () => {
    const markup = render(
      createElement(SectionOAuthBehavior, {
        ...props,
        values: { ...DEFAULT_VISUAL_VALUES, codexLiveMediaRelayICEServers: [server] },
        validationErrors: {
          codexStreamBootstrapTimeout: 'invalid_duration',
          codexLiveMediaRelayICEServers: 'invalid_ice_servers',
        },
      })
    );
    expect(markup).toContain(
      escapeText(translations.t('config_management.visual.validation.invalid_duration'))
    );
    expect(markup).toContain(
      escapeText(translations.t('config_management.visual.validation.invalid_ice_servers'))
    );
    const textarea = markup.match(/<textarea\b[^>]*>/)?.[0] ?? '';
    expect(textarea).toContain('aria-invalid="true"');
    const ids = textarea.match(/aria-describedby="([^"]+)"/)?.[1].split(' ') ?? [];
    expect(ids).toHaveLength(2);
    for (const id of ids) expect(markup).toContain(`id="${id}"`);
  });
});

import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import en from '@/i18n/locales/en.json';
import { BaseProviderForm } from '@/features/providers/sheets/forms/BaseProviderForm';
import { SponsorProviderForm } from '@/features/providers/sheets/forms/SponsorProviderForm';
import { CheckboxField } from '@/features/providers/sheets/forms/CheckboxField';
import { ResourceDetailView } from '@/features/providers/sheets/ResourceDetailView';
import { getSponsorProviderDefinition } from '@/features/providers/sponsorDefinitions';
import type {
  ProviderBrand,
  ProviderResource,
  SponsorProviderBrand,
} from '@/features/providers/types';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element));

const form = en.providersPage.form;
const sponsor = en.providersPage.sponsor;

/**
 * English copy of notes that were removed outright. Pinned as literals (not read from en.json)
 * because their keys are slated for deletion from the locales.
 */
const REMOVED = {
  baseUrlRequired: 'Required for this provider',
  disabled: "Disabled entries won't be used by the gateway",
  protocol: 'Select the group/protocol for this key.',
  protocolModels: 'config models. Fetch from',
};

const renderBase = (brand: ProviderBrand) =>
  render(
    createElement(BaseProviderForm, {
      brand,
      resource: null,
      mode: 'create',
      mutating: false,
      formId: 'f',
      onSubmit: async () => {},
    })
  );

const renderSponsor = (brand: SponsorProviderBrand) =>
  render(
    createElement(SponsorProviderForm, {
      brand,
      resource: null,
      mode: 'create',
      mutating: false,
      formId: 's',
      onSubmit: async () => {},
    })
  );

const infoTips = (html: string) =>
  [...html.matchAll(/<button\b[^>]*data-info-tip[^>]*>/g)].map((match) => match[0]);

/** The tip whose accessible name is "More information about <label>". */
const tipFor = (html: string, label: string) =>
  infoTips(html).find((tip) => tip.includes(`aria-label="More information about ${label}"`));

/** Id of the hidden copy that describes the InfoTip (and the control). */
const describedBy = (element: string) => /aria-describedby="([^"]+)"/.exec(element)?.[1];

/** The element (opening tag) whose id is `id` or, for useId-prefixed ids, ends in `-${id}`. */
const elementWithId = (html: string, id: string) =>
  new RegExp(`<[a-z]+\\b[^>]*\\bid="(?:[^"]*-)?${id}"[^>]*>`).exec(html)?.[0];

const escapeHtml = (text: string) =>
  text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/'/g, '&#x27;');

/**
 * Shared shape of every converted note: an InfoTip named after its label whose description
 * is a hidden copy of the note, and (when given) a control that points at the same copy.
 */
const expectNote = (html: string, label: string, note: string, controlId?: string) => {
  const tip = tipFor(html, label);
  expect(tip).toBeDefined();
  const hintId = describedBy(tip!)!;
  expect(html).toContain(`<span id="${hintId}" hidden="">${escapeHtml(note)}</span>`);
  if (controlId) expect(describedBy(elementWithId(html, controlId)!)).toBe(hintId);
};

/** No InfoTip may sit inside a <label>: it would leak into the control's accessible name. */
const expectNoTipInsideLabel = (html: string) => {
  for (const label of html.matchAll(/<label\b[^>]*>([\s\S]*?)<\/label>/g)) {
    expect(label[1]).not.toContain('data-info-tip');
  }
};

describe('provider form notes', () => {
  test('codex: required marker and disabled note removed; notes become InfoTips', () => {
    const html = renderBase('codex');
    expect(html).not.toContain(REMOVED.baseUrlRequired);
    expect(html).not.toContain(escapeHtml(REMOVED.disabled));
    expect(html).not.toContain('<small');
    // Labels carry only their text: no " · hint" suffix appended inside them.
    for (const label of html.matchAll(/<label\b[^>]*>([\s\S]*?)<\/label>/g)) {
      expect(label[1]).not.toContain(' · ');
    }
    // "Required" survives for assistive tech; validation still reports it on submit.
    expect(elementWithId(html, 'baseUrl')).toContain('aria-required="true"');
    expectNote(html, form.weight, form.weightHint, 'weight');
    expectNote(html, form.testModel, form.testModelHint, 'testModel');
    expectNoTipInsideLabel(html);
  });

  test('optional base URL is not marked required', () => {
    expect(elementWithId(renderBase('gemini'), 'baseUrl')).not.toContain('aria-required');
  });

  test('claude: fingerprint and behavior notes are InfoTips', () => {
    const html = renderBase('claude');
    expectNote(html, form.fingerprintProfile, form.fingerprintProfileHint, 'fingerprint-profile');
    expectNote(
      html,
      en.providersPage.behavior.rebuildMidSystemMessage,
      en.providersPage.behavior.rebuildMidSystemMessageHint
    );
    // The behavior checkbox keeps pointing at the same hidden copy as its InfoTip.
    const tip = tipFor(html, en.providersPage.behavior.rebuildMidSystemMessage)!;
    expect(html).toMatch(
      new RegExp(`<input type="checkbox"[^>]*aria-describedby="${describedBy(tip)}"`)
    );
    expectNoTipInsideLabel(html);
  });

  test('runtime policy section explanation rides on the cooling label', () => {
    const html = renderBase('gemini');
    const policy = en.providersPage.runtimePolicy;
    expect(html).not.toContain('<p ');
    expectNote(html, policy.cooling, policy.description);
    expectNote(html, policy.retry, policy.retryHint);
  });

  test('checkbox notes stay outside the label and describe the checkbox', () => {
    const html = render(
      createElement(CheckboxField, {
        label: form.modelImage,
        hint: form.modelImageHint,
        checked: false,
        disabled: true,
        onChange: () => {},
      })
    );
    expectNoTipInsideLabel(html);
    expectNote(html, form.modelImage, form.modelImageHint);
    const tip = tipFor(html, form.modelImage)!;
    expect(tip).not.toContain('disabled=""');
    expect(html).toMatch(
      new RegExp(
        `<input type="checkbox"[^>]*disabled=""[^>]*aria-describedby="${describedBy(tip)}"`
      )
    );
    // Without a note it is just the plain row.
    const plain = render(
      createElement(CheckboxField, { label: 'Plain', checked: true, onChange: () => {} })
    );
    expect(plain.startsWith('<label')).toBe(true);
    expect(plain).not.toContain('aria-describedby');
    expect(plain).not.toContain('data-info-tip');
  });

  test('sponsor form: restating notes removed, scope notes become InfoTips', () => {
    for (const brand of ['apikeyFun', 'fennoAI', 'qiniuCloud', 'aiCodeMirror', 'kimi'] as const) {
      const html = renderSponsor(brand);
      const definition = getSponsorProviderDefinition(brand);
      expect(html).not.toContain(REMOVED.protocol);
      expect(html).not.toContain(escapeHtml(REMOVED.disabled));
      expect(html).not.toContain(REMOVED.protocolModels);
      expectNote(html, form.apiKey, sponsor.apiKeyHint, 's-group-0-api-key');
      expectNote(html, form.weight, form.weightHint, 's-group-0-weight');
      if (definition.baseUrlOptions.length > 1) {
        const urlLabel = sponsor.urlMode.replace('{{provider}}', definition.displayName);
        expectNote(html, urlLabel, sponsor.urlHint);
        const group = /<div\b[^>]*role="radiogroup"[^>]*>/.exec(html)![0];
        expect(group).toContain('aria-labelledby="s-group-0-base-url-label"');
        expect(group).toContain('aria-describedby="s-group-0-base-url-hint"');
        expect(html).toContain(`id="s-group-0-base-url-label"`);
      } else {
        expect(html).not.toContain(sponsor.urlHint);
      }
      expectNoTipInsideLabel(html);
    }
  });

  test('sponsor detail explanation is an InfoTip after the resource name', () => {
    const resource = {
      id: 'fenno',
      brand: 'fennoAI',
      name: 'Fenno',
      identifier: 'fenno',
      raw: { openai: [], codex: [], claude: [], gemini: [] },
    } as unknown as ProviderResource;
    const html = render(createElement(ResourceDetailView, { resource }));
    const note = sponsor.detailHint.replace(
      '{{provider}}',
      getSponsorProviderDefinition('fennoAI').displayName
    );
    expect(html).not.toContain('<p ');
    expectNote(html, 'Fenno', note);
  });

  test('provider sheet header carries only the title', () => {
    const source = readFileSync(
      new URL('../src/features/providers/sheets/ProviderSheet.tsx', import.meta.url),
      'utf8'
    );
    expect(source).not.toMatch(/\beyebrow=/);
    expect(source).not.toMatch(/\bdescription=/);
    expect(source).toContain('title={titleText}');
  });
});

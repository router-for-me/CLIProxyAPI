import { beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { MaskedName } from '@/features/quota/components/MaskedName';
import { MASK_TEXT, maskedLabel } from '@/features/quota/maskedLabel';

const LABEL = 'user-c@example.com';

const renderName = (masked: boolean) =>
  renderToStaticMarkup(createElement(MaskedName, { label: LABEL, masked, ordinal: 3 }));

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

describe('MaskedName', () => {
  test('keeps the real name out of the masked markup', () => {
    const markup = renderName(true);

    expect(markup).not.toContain(LABEL);
    expect(markup).not.toContain('@');
    expect(markup).toContain(`<span aria-hidden="true">${MASK_TEXT}</span>`);
    expect(markup).toContain('Account 3');
    expect(markup).not.toContain('aria-label');
    expect(markup).not.toContain('title=');
  });

  test('shows the label when unmasked', () => {
    expect(renderName(false)).toBe(`<span>${LABEL}</span>`);
  });

  test('maskedLabel matches the screen-reader text', () => {
    expect(maskedLabel(i18n.t, 3)).toBe('Account 3');
  });
});

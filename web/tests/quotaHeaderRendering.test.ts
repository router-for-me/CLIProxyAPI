import { beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { QuotaHeader } from '@/features/quota/components/QuotaHeader';

const renderHeader = (showEmails: boolean) =>
  renderToStaticMarkup(
    createElement(QuotaHeader, {
      attentionCount: 0,
      disableControls: false,
      loadedCount: 2,
      onRefreshAll: () => undefined,
      onToggleEmails: () => undefined,
      refreshing: false,
      showEmails,
      totalCount: 2,
    })
  );

const emailsButton = (markup: string) =>
  markup.match(/<button[^>]*>(?:<[^>]+>)*(?:Show|Hide) emails<\/button>/)?.[0] ?? '';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

describe('QuotaHeader', () => {
  test('names the email toggle by its action without a contradicting pressed state', () => {
    const hidden = emailsButton(renderHeader(false));
    const shown = emailsButton(renderHeader(true));

    expect(hidden).toContain('Show emails');
    expect(shown).toContain('Hide emails');
    expect(hidden).not.toContain('aria-pressed');
    expect(shown).not.toContain('aria-pressed');
  });
});

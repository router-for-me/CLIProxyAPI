import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import '../src/i18n/index';
import { OAuthExcludedCard } from '../src/features/authFiles/components/OAuthExcludedCard';
import { OAuthModelAliasCard } from '../src/features/authFiles/components/OAuthModelAliasCard';

const noop = () => {};
const noopAsync = async () => {};

describe('OAuth configuration load guards', () => {
  test('disables excluded-model writes and exposes retry after a load failure', () => {
    const markup = renderToStaticMarkup(
      createElement(OAuthExcludedCard, {
        disableControls: false,
        excludedError: 'load',
        excluded: {},
        onRetry: noop,
        onAdd: noop,
        onEdit: noop,
        onDelete: noop,
      })
    );

    expect(markup).toContain('disabled=""');
    expect(markup).toContain('empty-action');
  });

  test('disables model-alias writes and exposes retry after a load failure', () => {
    const markup = renderToStaticMarkup(
      createElement(OAuthModelAliasCard, {
        disableControls: false,
        viewMode: 'list',
        onViewModeChange: noop,
        onRetry: noop,
        onAdd: noop,
        onEditProvider: noop,
        onDeleteProvider: noop,
        modelAliasError: 'load',
        modelAlias: {},
        allProviderModels: {},
        onUpdate: noopAsync,
        onDeleteLink: noop,
        onToggleFork: noopAsync,
        onRenameAlias: noopAsync,
        onDeleteAlias: noop,
      })
    );

    expect(markup).toContain('disabled=""');
    expect(markup).toContain('empty-action');
  });
});

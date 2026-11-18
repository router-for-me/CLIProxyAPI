import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { Select } from '@/components/ui/Select';

test('Provider advanced editors consistently use the shared Select instead of native menus', () => {
  for (const file of [
    'RuntimePolicyEditor',
    'ProviderBehaviorEditor',
    'ModelAdvancedFields',
    'ErrorRulesEditor',
  ]) {
    const source = readFileSync(
      new URL(`../src/features/providers/sheets/forms/${file}.tsx`, import.meta.url),
      'utf8'
    );
    expect(source).toContain("import { Select } from '@/components/ui/Select'");
    expect(source).toContain('<Select');
    expect(source).not.toMatch(/<select[\s>]/);
    expect(source).not.toMatch(/<option[\s>]/);
  }
});

test('shared Select forwards validation, labels and disabled state to its trigger', () => {
  const markup = renderToStaticMarkup(
    createElement(Select, {
      id: 'rule-action',
      value: 'stop',
      options: [{ value: 'stop', label: 'Stop' }],
      onChange: () => {},
      ariaLabel: 'Action',
      ariaLabelledBy: 'action-label',
      ariaDescribedBy: 'action-error',
      ariaInvalid: true,
      disabled: true,
    })
  );
  expect(markup).not.toContain('<select');
  expect(markup).toContain('id="rule-action"');
  expect(markup).toContain('aria-haspopup="listbox"');
  expect(markup).toContain('aria-expanded="false"');
  expect(markup).toContain('aria-label="Action"');
  expect(markup).toContain('aria-labelledby="action-label"');
  expect(markup).toContain('aria-describedby="action-error"');
  expect(markup).toContain('aria-invalid="true"');
  expect(markup).toContain('disabled=""');
  expect(markup).toContain('>Stop</span>');
});

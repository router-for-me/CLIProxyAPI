/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { expect, test, vi } from 'vitest'

import fr from '@/i18n/locales/fr.json'

import { ModelMappingBatchDialog } from '../model-mapping-batch-dialog'

test('creating user aliases starts unselected, previews request-to-upstream pairs, and skips unchanged names', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={vi.fn()}
      upstreamModels={['gpt-4o-all', 'o3']}
      channelModels={['gpt-4o-all']}
      onApply={onApply}
    />
  )
  expect(
    screen.getByRole('radio', { name: 'Create aliases for users' })
  ).toBeChecked()
  expect(screen.getByRole('combobox', { name: 'Rule' })).toHaveTextContent(
    'Strip suffix'
  )
  expect(screen.getByRole('checkbox', { name: 'gpt-4o-all' })).not.toBeChecked()
  expect(
    screen.getByRole('button', { name: 'Add 0 mapping(s)' })
  ).toBeDisabled()
  expect(
    screen.getByText(
      'Example: users call gpt-4o; upstream receives gpt-4o-all.'
    )
  ).toBeVisible()

  await user.click(screen.getByRole('checkbox', { name: 'gpt-4o-all' }))
  await user.click(screen.getByRole('checkbox', { name: 'o3' }))
  expect(screen.getByText('Enter a rule to preview mappings.')).toBeVisible()
  expect(screen.queryByText(/unchanged by the rule/)).not.toBeInTheDocument()
  await user.type(screen.getByRole('textbox', { name: 'Suffix' }), '-all')
  const preview = within(screen.getByRole('list', { name: 'Preview' }))
  expect(preview.getByRole('listitem')).toHaveTextContent('gpt-4ogpt-4o-all')
  expect(
    screen.getByText('1 model(s) unchanged by the rule were skipped')
  ).toBeVisible()
  expect(onApply).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(onApply).toHaveBeenCalledWith({
    pairs: [{ from: 'gpt-4o', to: 'gpt-4o-all' }],
    syncModels: true,
  })
})

test('changing upstream names preserves the request name and leaves model-list syncing off', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={vi.fn()}
      channelModels={['gemini-2.5-flash']}
      onApply={onApply}
    />
  )
  expect(
    screen.getByRole('radio', { name: 'Change upstream model names' })
  ).toBeChecked()
  expect(
    screen.queryByRole('checkbox', { name: /Publish the request names/ })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('checkbox', { name: 'gemini-2.5-flash' }))
  await user.type(screen.getByRole('textbox', { name: 'Suffix' }), '-all')
  await user.click(screen.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(onApply).toHaveBeenCalledWith({
    pairs: [{ from: 'gemini-2.5-flash', to: 'gemini-2.5-flash-all' }],
    syncModels: false,
  })
})

test('switching the model list clears selection without changing the chosen task or naming rule', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={vi.fn()}
      upstreamModels={['gpt-4o-all']}
      channelModels={['claude-all']}
      onApply={onApply}
    />
  )
  await user.click(screen.getByRole('checkbox', { name: 'gpt-4o-all' }))
  await user.type(screen.getByRole('textbox', { name: 'Suffix' }), '-all')
  await user.click(screen.getByRole('tab', { name: /Channel models/ }))
  expect(
    screen.getByRole('radio', { name: 'Create aliases for users' })
  ).toBeChecked()
  expect(screen.getByRole('textbox', { name: 'Suffix' })).toHaveValue('-all')
  expect(
    screen.getByRole('button', { name: 'Add 0 mapping(s)' })
  ).toBeDisabled()
  await user.click(screen.getByRole('checkbox', { name: 'claude-all' }))
  await user.click(screen.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(onApply).toHaveBeenCalledWith({
    pairs: [{ from: 'claude', to: 'claude-all' }],
    syncModels: true,
  })
})

test('creating aliases from channel models reports conflicts and allows opting out of model-list syncing', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={vi.fn()}
      channelModels={['claude-all', 'claude-latest']}
      onApply={onApply}
    />
  )
  expect(screen.queryByRole('tab')).not.toBeInTheDocument()
  await user.click(
    screen.getByRole('radio', { name: 'Create aliases for users' })
  )
  await user.click(screen.getByRole('checkbox', { name: 'claude-all' }))
  await user.click(screen.getByRole('checkbox', { name: 'claude-latest' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Suffix' }),
    '-all, -latest'
  )
  expect(
    screen.getByText(
      '1 model(s) skipped because another model derives the same request name'
    )
  ).toBeVisible()
  await user.click(
    screen.getByRole('checkbox', { name: /Publish the request names/ })
  )
  await user.click(screen.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(onApply).toHaveBeenCalledWith({
    pairs: [{ from: 'claude', to: 'claude-all' }],
    syncModels: false,
  })
})

test('the alias task can add a prefix and only shows fields for the selected rule', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={vi.fn()}
      upstreamModels={['gpt-4o']}
      channelModels={[]}
      onApply={onApply}
    />
  )
  await user.click(screen.getByRole('checkbox', { name: 'gpt-4o' }))
  expect(
    screen.queryByRole('textbox', { name: 'Prefix' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('combobox', { name: 'Rule' }))
  await user.click(screen.getByRole('option', { name: 'Add prefix' }))
  expect(
    screen.queryByRole('textbox', { name: 'Suffix' })
  ).not.toBeInTheDocument()
  await user.type(screen.getByRole('textbox', { name: 'Prefix' }), 'team/')
  await user.click(screen.getByRole('button', { name: 'Add 1 mapping(s)' }))
  expect(onApply).toHaveBeenCalledWith({
    pairs: [{ from: 'team/gpt-4o', to: 'gpt-4o' }],
    syncModels: true,
  })
})

test('replacement previews include every selected model even when several share an upstream target', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  const models = [
    'gpt-4o-a',
    'gpt-4oa',
    ...Array.from({ length: 8 }, (_, n) => `gpt-${n}`),
  ]
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={vi.fn()}
      channelModels={models}
      onApply={onApply}
    />
  )
  await user.click(
    screen.getByRole('checkbox', { name: 'Select all models in OpenAI' })
  )
  await user.click(screen.getByRole('combobox', { name: 'Rule' }))
  await user.click(screen.getByRole('option', { name: 'Replace text' }))
  await user.type(screen.getByRole('textbox', { name: 'Find' }), '-')
  const preview = within(screen.getByRole('list', { name: 'Preview' }))
  expect(preview.getAllByRole('listitem')).toHaveLength(10)
  expect(preview.getAllByText('gpt4oa')).toHaveLength(2)
  await user.click(screen.getByRole('button', { name: 'Add 10 mapping(s)' }))
  expect(onApply).toHaveBeenCalledWith({
    pairs: models.map((from) => ({ from, to: from.replaceAll('-', '') })),
    syncModels: false,
  })
})

test('cancelling after configuring a rule does not apply mappings', async () => {
  const user = userEvent.setup()
  const onApply = vi.fn()
  const onOpenChange = vi.fn()
  render(
    <ModelMappingBatchDialog
      open
      onOpenChange={onOpenChange}
      channelModels={['gpt-4o']}
      onApply={onApply}
    />
  )
  await user.click(screen.getByRole('checkbox', { name: 'gpt-4o' }))
  await user.type(screen.getByRole('textbox', { name: 'Suffix' }), '-all')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(onOpenChange).toHaveBeenCalledWith(false)
  expect(onApply).not.toHaveBeenCalled()
})

test('long translated source labels wrap inside their row without covering the model search', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'fr', resources: { fr }, keySeparator: false })
  render(
    <I18nextProvider i18n={i18n}>
      <ModelMappingBatchDialog
        open
        onOpenChange={vi.fn()}
        upstreamModels={['gpt-4o-all']}
        channelModels={['gpt-4o']}
        onApply={vi.fn()}
      />
    </I18nextProvider>
  )
  const tabs = screen.getByRole('tablist', { name: 'Sélectionner les modèles' })
  expect(tabs).toHaveClass('grid-cols-2', 'group-data-horizontal/tabs:h-auto')
  for (const tab of within(tabs).getAllByRole('tab')) {
    expect(tab).toHaveClass('whitespace-normal', 'h-auto')
  }
  expect(
    screen.getByRole('textbox', { name: 'Rechercher des modèles...' })
  ).toBeVisible()
})

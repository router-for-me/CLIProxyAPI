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
import { fireEvent, render, within } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

const { useState } = await import('react')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { AutoGroupOrderEditor } = await import('../auto-group-order-editor')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        '{{count}} / {{max}} groups selected':
          '{{count}} / {{max}} groups selected',
        'Add Auto group': 'Add Auto group',
        'Auto group order': 'Auto group order',
        'Drag {{group}} to reorder': 'Drag {{group}} to reorder',
        'Inherit global Auto order': 'Inherit global Auto order',
        'Maximum {{max}} groups selected': 'Maximum {{max}} groups selected',
        'Move {{group}} down': 'Move {{group}} down',
        'Move {{group}} up': 'Move {{group}} up',
        'No available groups in the global Auto order.':
          'No available groups in the global Auto order.',
        'No valid custom Auto groups remain. Add a group or restore global Auto.':
          'No valid custom Auto groups remain. Add a group or restore global Auto.',
        'No custom groups. Saving will inherit the complete global Auto order.':
          'No custom groups. Saving will inherit the complete global Auto order.',
        'Remove {{group}}': 'Remove {{group}}',
        'Restore global Auto': 'Restore global Auto',
        Ratio: 'Ratio',
        'Search...': 'Search...',
        'No group found.': 'No group found.',
        'Select a group': 'Select a group',
        'Using the complete global Auto order ({{count}} groups)':
          'Using the complete global Auto order ({{count}} groups)',
      },
    },
  },
})

const globalOptions = [
  { value: 'vip', label: 'VIP', desc: 'Priority access', ratio: 3 },
  { value: 'default', label: 'Default', desc: 'Standard access', ratio: 1 },
  { value: 'team', label: 'Team', desc: 'Shared access', ratio: 2 },
]

function Harness(props: { initialGroups?: string[] }) {
  const [groups, setGroups] = useState(
    props.initialGroups ?? ['default', 'vip']
  )
  const [mode, setMode] = useState<'inherit' | 'custom'>('custom')
  return (
    <I18nextProvider i18n={i18n}>
      <AutoGroupOrderEditor
        value={groups}
        mode={mode}
        options={[
          { value: 'auto', label: 'auto' },
          { value: 'default', label: 'default', ratio: 1 },
          { value: 'vip', label: 'vip', ratio: 2 },
          { value: 'team', label: 'team', ratio: 3 },
        ]}
        globalOptions={globalOptions}
        maxCount={2}
        onChange={(value) => {
          setGroups(value.groups)
          setMode(value.mode)
        }}
      />
      <output data-testid='order'>{groups.join(',')}</output>
      <output data-testid='mode'>{mode}</output>
    </I18nextProvider>
  )
}

function InheritanceHarness(props: { globalOptions?: typeof globalOptions }) {
  const [groups, setGroups] = useState<string[]>([])
  const [mode, setMode] = useState<'inherit' | 'custom'>('inherit')

  return (
    <I18nextProvider i18n={i18n}>
      <AutoGroupOrderEditor
        value={groups}
        mode={mode}
        options={[{ value: 'auto', label: 'auto' }, ...globalOptions]}
        globalOptions={props.globalOptions ?? globalOptions}
        maxCount={2}
        onChange={(value) => {
          setGroups(value.groups)
          setMode(value.mode)
        }}
      />
      <output data-testid='order'>{groups.join(',')}</output>
      <output data-testid='mode'>{mode}</output>
    </I18nextProvider>
  )
}

function CustomEmptyHarness() {
  const [groups, setGroups] = useState<string[]>([])
  const [mode, setMode] = useState<'inherit' | 'custom'>('custom')

  return (
    <I18nextProvider i18n={i18n}>
      <AutoGroupOrderEditor
        value={groups}
        mode={mode}
        options={[{ value: 'auto', label: 'auto' }, ...globalOptions]}
        globalOptions={globalOptions}
        maxCount={2}
        onChange={(value) => {
          setGroups(value.groups)
          setMode(value.mode)
        }}
      />
      <output data-testid='order'>{groups.join(',')}</output>
      <output data-testid='mode'>{mode}</output>
    </I18nextProvider>
  )
}

function findButton(container: HTMLElement, label: string): HTMLButtonElement {
  return within(container).getByRole('button', { name: label })
}

function getCommandItem(label: string): HTMLElement {
  const item = [
    ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
  ].find((candidate) => candidate.textContent?.includes(label))
  if (!item) {
    throw new Error(`Expected command item containing "${label}"`)
  }
  return item
}

describe('Auto group order editor', () => {
  test('enforces the limit and exposes accessible reorder controls', () => {
    const { container } = render(<Harness />)

    const addButton = within(container).getByRole('combobox')
    expect(addButton).toBeDisabled()
    expect(container).toHaveTextContent('2 / 2 groups selected')
    expect(
      within(container).getByRole('group', { name: 'Auto group order' })
    ).toBeInTheDocument()
    expect(findButton(container, 'Drag default to reorder').type).toBe('button')

    fireEvent.click(findButton(container, 'Move default down'))
    expect(within(container).getByTestId('order')).toHaveTextContent(
      'vip,default'
    )

    fireEvent.keyDown(findButton(container, 'Drag vip to reorder'), {
      key: 'ArrowDown',
    })
    expect(within(container).getByTestId('order')).toHaveTextContent(
      'default,vip'
    )
  })

  test('adds and removes groups, then restores inheritance as an empty value', () => {
    const { container } = render(<Harness />)
    fireEvent.click(findButton(container, 'Remove vip'))

    expect(within(container).getByTestId('order')).toHaveTextContent('default')
    const addButton = within(container).getByRole('combobox')
    expect(addButton).toBeEnabled()

    fireEvent.click(addButton)
    fireEvent.click(getCommandItem('team'))
    expect(within(container).getByTestId('order')).toHaveTextContent(
      'default,team'
    )
    expect(addButton).toBeDisabled()

    fireEvent.click(
      within(container).getByRole('button', { name: 'Restore global Auto' })
    )

    expect(within(container).getByTestId('order')).toBeEmptyDOMElement()
    expect(within(container).getByTestId('mode')).toHaveTextContent('inherit')
    expect(container).toHaveTextContent(
      'Using the complete global Auto order (3 groups)'
    )

    const inheritedItems = container.querySelectorAll(
      '[data-slot="global-auto-order"] > li'
    )
    expect(
      [...inheritedItems].map(
        (item) =>
          item.querySelector('[data-slot="global-auto-order-name"]')
            ?.textContent
      )
    ).toEqual(['VIP', 'Default', 'Team'])
  })

  test('shows the complete inherited order with metadata beyond the custom limit', () => {
    const { container } = render(<InheritanceHarness />)

    expect(container).toHaveTextContent(
      'Using the complete global Auto order (3 groups)'
    )
    expect(container).not.toHaveTextContent('0 / 2 groups selected')

    const order = container.querySelector<HTMLOListElement>(
      '[data-slot="global-auto-order"]'
    )
    if (!order) {
      throw new Error('Expected inherited Auto group order')
    }
    expect(order).toHaveClass('overflow-y-auto', 'flex-wrap')

    const items = [...order.querySelectorAll('li')]
    expect(items.length).toBe(3)
    expect(
      order.querySelectorAll('[data-slot="global-auto-order-connector"]').length
    ).toBe(2)
    expect(
      items.map((item) => ({
        index: item.querySelector('[data-slot="global-auto-order-index"]')
          ?.textContent,
        name: item.querySelector('[data-slot="global-auto-order-name"]')
          ?.textContent,
        title: item
          .querySelector('[data-slot="global-auto-order-chip"]')
          ?.getAttribute('title'),
        description: item.querySelector(
          '[data-slot="global-auto-order-description"]'
        )?.textContent,
        ratio: item.querySelector('[data-slot="badge"]')?.textContent,
      }))
    ).toEqual([
      {
        index: '1',
        name: 'VIP',
        title: 'Priority access',
        description: 'Priority access',
        ratio: '3x',
      },
      {
        index: '2',
        name: 'Default',
        title: 'Standard access',
        description: 'Standard access',
        ratio: '1x',
      },
      {
        index: '3',
        name: 'Team',
        title: 'Shared access',
        description: 'Shared access',
        ratio: '2x',
      },
    ])

    for (const item of items) {
      const chip = item.querySelector('[data-slot="global-auto-order-chip"]')
      expect(chip).toBeInTheDocument()
      const description = item.querySelector(
        '[data-slot="global-auto-order-description"]'
      )
      expect(description).toHaveClass('sr-only')
    }

    expect(
      items[0]?.querySelector('[data-slot="global-auto-order-connector"]')
    ).toBe(null)
    for (const item of items.slice(1)) {
      const connector = item.querySelector(
        '[data-slot="global-auto-order-connector"]'
      )
      expect(connector).toHaveAttribute('aria-hidden', 'true')
    }

    expect(container.querySelector('[aria-label^="Drag "]')).toBe(null)
    expect(container.querySelector('[aria-label^="Move "]')).toBe(null)
    expect(container.querySelector('[aria-label^="Remove "]')).toBe(null)

    expect(
      within(container).getByRole('button', { name: 'Restore global Auto' })
    ).toBeDisabled()
  })

  test('shows an explicit empty state when the global Auto order has no groups', () => {
    const { container } = render(<InheritanceHarness globalOptions={[]} />)

    expect(container).toHaveTextContent(
      'Using the complete global Auto order (0 groups)'
    )
    expect(container).toHaveTextContent(
      'No available groups in the global Auto order.'
    )
    expect(container.querySelector('[data-slot="global-auto-order"]')).toBe(
      null
    )
  })

  test('keeps an empty custom order distinct from global inheritance', () => {
    const { container } = render(<CustomEmptyHarness />)

    expect(within(container).getByTestId('mode')).toHaveTextContent('custom')
    expect(container).toHaveTextContent(
      'No valid custom Auto groups remain. Add a group or restore global Auto.'
    )
    expect(container.querySelector('[data-slot="global-auto-order"]')).toBe(
      null
    )

    const restoreButton = within(container).getByRole('button', {
      name: 'Restore global Auto',
    })
    expect(restoreButton).toBeEnabled()
    fireEvent.click(restoreButton)

    expect(within(container).getByTestId('mode')).toHaveTextContent('inherit')
    expect(
      container.querySelector('[data-slot="global-auto-order"]')
    ).toBeInTheDocument()
  })

  test('adding a group from inheritance explicitly creates a custom order', () => {
    const { container } = render(<InheritanceHarness />)

    fireEvent.click(within(container).getByRole('combobox'))
    fireEvent.click(getCommandItem('VIP'))

    expect(within(container).getByTestId('mode')).toHaveTextContent('custom')
    expect(within(container).getByTestId('order')).toHaveTextContent('vip')
    expect(container.querySelector('[data-slot="global-auto-order"]')).toBe(
      null
    )
  })

  test('removing the last custom group does not silently enable inheritance', () => {
    const { container } = render(<Harness initialGroups={['default']} />)
    fireEvent.click(findButton(container, 'Remove default'))

    expect(within(container).getByTestId('order')).toBeEmptyDOMElement()
    expect(within(container).getByTestId('mode')).toHaveTextContent('custom')
    expect(container).toHaveTextContent(
      'No valid custom Auto groups remain. Add a group or restore global Auto.'
    )
  })
})

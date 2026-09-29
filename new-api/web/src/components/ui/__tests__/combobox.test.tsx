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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'

import { Dialog } from '@/components/dialog'

import { Combobox } from '../combobox'
import { Sheet, SheetContent, SheetTitle } from '../sheet'

const options = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Google' },
  { value: 'disabled', label: 'Unavailable provider', disabled: true },
]

function Fixture() {
  const [value, setValue] = useState('openai')
  return (
    <>
      <Combobox
        options={options}
        value={value}
        onValueChange={(next) => setValue(next ?? '')}
        aria-label='Provider'
        emptyText='No matching provider'
      />
      <output>{value}</output>
    </>
  )
}

describe('searchable single selection', () => {
  it.each(['dialog', 'sheet'] as const)(
    'keeps the options closed when a %s autofocuses the input, then opens on click',
    async (container) => {
      const field = <Fixture />
      render(
        container === 'dialog' ? (
          <Dialog open title='Choose a provider'>
            {field}
          </Dialog>
        ) : (
          <Sheet open>
            <SheetContent>
              <SheetTitle>Choose a provider</SheetTitle>
              {field}
            </SheetContent>
          </Sheet>
        )
      )
      const user = userEvent.setup()
      const input = screen.getByRole('combobox', { name: 'Provider' })
      await waitFor(() => expect(input).toHaveFocus())
      expect(input).toHaveAttribute('aria-expanded', 'false')
      expect(input).toHaveValue('OpenAI')
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument()

      await user.click(input)
      expect(screen.getByRole('option', { name: 'Google' })).toBeVisible()
      await user.click(screen.getByRole('option', { name: 'Google' }))
      await waitFor(() => expect(input).toHaveValue('Google'))
      expect(input).toHaveAttribute('aria-expanded', 'false')
      expect(screen.getByRole('dialog')).toBeVisible()
    }
  )

  it('keeps options closed on Tab focus and opens them with the arrow key', async () => {
    render(<Fixture />)
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Provider' })
    await user.tab()
    expect(input).toHaveFocus()
    expect(input).toHaveAttribute('aria-expanded', 'false')
    await user.keyboard('{ArrowDown}')
    expect(screen.getByRole('option', { name: 'Google' })).toBeVisible()
    await user.keyboard('{Escape}')
    expect(input).toHaveAttribute('aria-expanded', 'false')
    expect(input).toHaveValue('OpenAI')
  })

  it('opens and filters when typing into a focused, closed input', async () => {
    render(<Fixture />)
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Provider' })
    await user.tab()
    await user.keyboard('g')
    expect(input).toHaveValue('g')
    expect(screen.getByRole('option', { name: 'Google' })).toBeVisible()
    expect(
      screen.queryByRole('option', { name: 'OpenAI' })
    ).not.toBeInTheDocument()
  })

  it.each([true, false])(
    'honors explicit openOnFocus=%s',
    async (openOnFocus) => {
      render(
        <Combobox
          options={options}
          value='openai'
          aria-label='Provider'
          openOnFocus={openOnFocus}
        />
      )
      const user = userEvent.setup()
      const input = screen.getByRole('combobox', { name: 'Provider' })
      await user.tab()
      expect(input).toHaveFocus()
      expect(input).toHaveAttribute('aria-expanded', String(openOnFocus))
      if (!openOnFocus) {
        await user.click(screen.getByRole('button', { name: 'Provider' }))
        expect(screen.getByRole('option', { name: 'Google' })).toBeVisible()
      }
    }
  )

  it('searches labels and values without committing text, shows empty results, and restores the selection on Escape', async () => {
    render(<Fixture />)
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Provider' })
    expect(input).toHaveValue('OpenAI')
    await user.click(input)
    await user.type(input, 'missing')
    expect(screen.getByText('No matching provider')).toBeVisible()
    expect(screen.getByText('openai')).toHaveTextContent('openai')
    await user.keyboard('{Escape}')
    expect(input).toHaveValue('OpenAI')
    await user.click(input)
    await user.type(input, 'gemini')
    expect(screen.getByRole('option', { name: 'Google' })).toBeVisible()
    await user.keyboard('{ArrowDown}{Enter}')
    await waitFor(() => expect(input).toHaveValue('Google'))
    expect(screen.getByText('gemini')).toHaveTextContent('gemini')
  })

  it('respects disabled controls and options', async () => {
    const change = vi.fn()
    const view = render(
      <Combobox
        options={options}
        value='openai'
        onValueChange={change}
        aria-label='Provider'
        disabled
      />
    )
    const user = userEvent.setup()
    expect(screen.getByRole('combobox', { name: 'Provider' })).toBeDisabled()
    view.rerender(
      <Combobox
        options={options}
        value='openai'
        onValueChange={change}
        aria-label='Provider'
      />
    )
    await user.click(screen.getByRole('combobox', { name: 'Provider' }))
    expect(
      screen.getByRole('option', { name: 'Unavailable provider' })
    ).toHaveAttribute('aria-disabled', 'true')
    await user.click(
      screen.getByRole('option', { name: 'Unavailable provider' })
    )
    expect(change).not.toHaveBeenCalled()
  })
})

const pluginOptions = [
  {
    value: 'alpha',
    label: 'Alpha plugin',
    icon: <img src='/api/plugin/task/alpha/icon' alt='' />,
  },
  {
    value: 'beta',
    label: 'Beta plugin',
    icon: <img src='/api/plugin/task/beta/icon' alt='' />,
  },
]

function PluginSelectionFixture() {
  const [value, setValue] = useState<string | null>('alpha')
  return (
    <Combobox
      options={pluginOptions}
      value={value}
      onValueChange={setValue}
      showSelectedIcon
      aria-label='Task plugin'
    />
  )
}

describe('selected option icons', () => {
  it('shows the selected plugin logo and updates it when choosing another plugin', async () => {
    render(<PluginSelectionFixture />)
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Task plugin' })
    expect(screen.getByAltText('')).toHaveAttribute(
      'src',
      '/api/plugin/task/alpha/icon'
    )
    await user.click(input)
    const nextOption = screen.getByRole('option', { name: 'Beta plugin' })
    expect(nextOption.querySelector('img')).toHaveAttribute(
      'src',
      '/api/plugin/task/beta/icon'
    )
    await user.click(nextOption)
    await waitFor(() => expect(input).toHaveValue('Beta plugin'))
    expect(screen.getByAltText('')).toHaveAttribute(
      'src',
      '/api/plugin/task/beta/icon'
    )
  })

  it('removes the logo when the selection is cleared or no longer has an icon', () => {
    const view = render(
      <Combobox
        options={pluginOptions}
        value='alpha'
        showSelectedIcon
        aria-label='Task plugin'
      />
    )
    expect(screen.getByAltText('')).toBeInTheDocument()
    view.rerender(
      <Combobox
        options={pluginOptions}
        value={null}
        showSelectedIcon
        aria-label='Task plugin'
      />
    )
    expect(screen.queryByAltText('')).not.toBeInTheDocument()
    view.rerender(
      <Combobox
        options={[{ value: 'alpha', label: 'Alpha plugin' }]}
        value='alpha'
        showSelectedIcon
        aria-label='Task plugin'
      />
    )
    expect(screen.queryByAltText('')).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Task plugin' })).toHaveValue(
      'Alpha plugin'
    )
  })

  it('preserves the existing text-only selected state unless icon display is requested', () => {
    render(
      <Combobox
        options={pluginOptions}
        value='alpha'
        aria-label='Task plugin'
      />
    )
    expect(screen.queryByAltText('')).not.toBeInTheDocument()
  })
})

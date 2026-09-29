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
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { Combobox } from '../combobox'
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerTitle,
} from '../drawer'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../select'

const options = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Google' },
]

function ProviderCombobox(props: {
  onValueChange: (value: string | null) => void
}) {
  return (
    <Combobox
      options={options}
      value='openai'
      onValueChange={props.onValueChange}
      aria-label='Provider'
    />
  )
}

function ProviderSelect(props: {
  onValueChange: (value: string | null) => void
}) {
  return (
    <Select value='openai' onValueChange={props.onValueChange}>
      <SelectTrigger aria-label='Provider'>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function FilterDrawer(props: { children: ReactNode }) {
  return (
    <Drawer open>
      <DrawerContent>
        <DrawerTitle>Filters</DrawerTitle>
        <DrawerDescription>Adjust the result filters</DrawerDescription>
        {props.children}
      </DrawerContent>
    </Drawer>
  )
}

// JSDOM does not implement the pointer-capture API used by the drawer.
const pointerCapture = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: () => undefined,
  })
})

afterEach(() => {
  if (pointerCapture) {
    Object.defineProperty(
      HTMLElement.prototype,
      'setPointerCapture',
      pointerCapture
    )
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
  }
})

describe('popups inside a drawer', () => {
  it('renders combobox options inside the drawer dialog and clicking one applies it without closing the drawer', async () => {
    const change = vi.fn()
    render(
      <FilterDrawer>
        <ProviderCombobox onValueChange={change} />
      </FilterDrawer>
    )
    const user = userEvent.setup()
    const dialog = screen.getByRole('dialog', { name: 'Filters' })

    await user.click(within(dialog).getByRole('combobox', { name: 'Provider' }))
    await user.click(
      await within(dialog).findByRole('option', { name: 'Google' })
    )

    expect(change).toHaveBeenCalledWith('gemini')
    expect(screen.getByRole('dialog', { name: 'Filters' })).toBeInTheDocument()
  })

  it('renders select options inside the drawer dialog and clicking one applies it without closing the drawer', async () => {
    const change = vi.fn()
    render(
      <FilterDrawer>
        <ProviderSelect onValueChange={change} />
      </FilterDrawer>
    )
    const user = userEvent.setup()
    const dialog = screen.getByRole('dialog', { name: 'Filters' })

    await user.click(within(dialog).getByRole('combobox', { name: 'Provider' }))
    await user.click(
      await within(dialog).findByRole('option', { name: 'Google' })
    )

    expect(change).toHaveBeenCalledWith('gemini', expect.anything())
    expect(screen.getByRole('dialog', { name: 'Filters' })).toBeInTheDocument()
  })
})

describe('popups outside a drawer', () => {
  it('portals combobox options to document.body outside the component subtree', async () => {
    const view = render(<ProviderCombobox onValueChange={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('combobox', { name: 'Provider' }))
    const listbox = await screen.findByRole('listbox')

    expect(document.body).toContainElement(listbox)
    expect(view.container).not.toContainElement(listbox)
  })

  it('portals select options to document.body outside the component subtree', async () => {
    const view = render(<ProviderSelect onValueChange={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('combobox', { name: 'Provider' }))
    const listbox = await screen.findByRole('listbox')

    expect(document.body).toContainElement(listbox)
    expect(view.container).not.toContainElement(listbox)
  })
})

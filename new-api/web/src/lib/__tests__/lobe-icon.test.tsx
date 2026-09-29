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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { runInNewContext } from 'node:vm'

import { act, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { getLobeIcon, getLobeIconNames } from '../lobe-icon'

vi.mock('@lobehub/icons/es/Mistral/components/Color.js', () => {
  throw new Error('Icon chunk unavailable')
})

describe('Lobe icons', () => {
  it.each([
    '/build/web/node_modules/@lobehub/icons/es/',
    'D:\\a\\new-api\\web\\node_modules\\@lobehub\\icons\\es\\',
  ])('includes icon variants when bundling from %s', (root) => {
    // Vitest loads imports without applying Rspack's webpackInclude filter.
    const source = readFileSync(
      resolve(import.meta.dirname, '../lobe-icon.tsx'),
      'utf8'
    )
    const comment = source.match(/\/\*\s*(webpackInclude:[\s\S]*?)\*\//)
    expect(comment).not.toBeNull()
    const { webpackInclude } = runInNewContext(
      `({${comment?.[1]}})`,
      {},
      {
        timeout: 1000,
      }
    ) as { webpackInclude: RegExp }
    const separator = root.includes('\\') ? '\\' : '/'
    const files = [
      'OpenAI/components/Mono.js',
      'Claude/components/Color.js',
      'Gemini/components/Color.js',
      'Gemma/components/Simple.js',
      'LobeHub/components/Morden.js',
      'OpenAI/index.js',
      'OpenAI/components/Mono.d.ts',
      'OpenAI/components/Unknown.js',
    ]
    const included = files.filter((file) =>
      webpackInclude.test(root + file.replaceAll('/', separator))
    )
    expect(included).toEqual(files.slice(0, 5))
  })

  it('loads a named variant with its configured size and accessibility props', async () => {
    render(
      getLobeIcon('Claude.Color.size={32}.role="img".aria-label="Claude icon"')
    )
    expect(screen.getByText('C')).toHaveStyle({ width: '32px', height: '32px' })
    const icon = await screen.findByRole('img', { name: 'Claude icon' })
    expect(icon.tagName.toLowerCase()).toBe('svg')
    expect(icon).toHaveAttribute('width', '32')
    expect(icon).toHaveAttribute('height', '32')
  })

  it('updates the displayed icon when the name changes', async () => {
    const { rerender } = render(
      getLobeIcon('OpenAI.role="img".aria-label="OpenAI icon"', 24)
    )
    expect(
      await screen.findByRole('img', { name: 'OpenAI icon' })
    ).toHaveAttribute('width', '24')
    rerender(
      getLobeIcon('Gemini.Color.role="img".aria-label="Gemini icon"', 28)
    )
    expect(
      await screen.findByRole('img', { name: 'Gemini icon' })
    ).toHaveAttribute('width', '28')
    expect(
      screen.queryByRole('img', { name: 'OpenAI icon' })
    ).not.toBeInTheDocument()
  })

  it('falls back to the base icon when the requested variant is unavailable', async () => {
    render(getLobeIcon('OpenAI.Unknown.role="img".aria-label="OpenAI icon"'))
    expect(
      await screen.findByRole('img', { name: 'OpenAI icon' })
    ).toBeVisible()
  })

  it.each(['Gemma.Simple', 'LobeHub.Morden'])(
    'loads the %s variant that is not listed in the standard catalog flags',
    async (name) => {
      render(getLobeIcon(`${name}.role="img".aria-label="Special icon"`, 24))
      expect(
        await screen.findByRole('img', { name: 'Special icon' })
      ).toHaveAttribute('height', '24')
    }
  )

  it('keeps a sized placeholder when a chunk fails and accepts later size changes', async () => {
    const { rerender } = render(getLobeIcon('Mistral.Color', 18))
    await act(async () => {
      await vi.dynamicImportSettled()
    })
    expect(screen.getByText('M')).toHaveStyle({ width: '18px', height: '18px' })
    rerender(getLobeIcon('Mistral.Color', 36))
    expect(screen.getByText('M')).toHaveStyle({ width: '36px', height: '36px' })
  })

  it('keeps placeholders for missing names and preserves custom icons', () => {
    const { rerender } = render(getLobeIcon('NotAnInstalledIcon', 30))
    expect(screen.getByText('N')).toHaveStyle({ width: '30px', height: '30px' })
    rerender(getLobeIcon('  '))
    expect(screen.getByText('?')).toBeVisible()
    rerender(getLobeIcon('SGLang', 24))
    expect(screen.getByRole('presentation', { hidden: true })).toHaveAttribute(
      'width',
      '24'
    )
    expect(getLobeIconNames()).toEqual(
      expect.arrayContaining([
        'OpenAI',
        'Claude.Color',
        'SGLang',
        'Sub2API',
        'Wan',
      ])
    )
  })
})

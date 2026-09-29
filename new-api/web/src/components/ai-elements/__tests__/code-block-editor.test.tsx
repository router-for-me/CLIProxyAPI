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
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import { CodeBlockEditor } from '../code-block'

afterEach(() => {
  cleanup()
})

function editorTree(value: string) {
  // A fresh inline onKeyDown per call mirrors PlaygroundMessageEditor, which
  // recreates its handler on every keystroke-driven render.
  return (
    <CodeBlockEditor
      ariaLabel='Edit message'
      language='markdown'
      onChange={() => undefined}
      onKeyDown={() => undefined}
      value={value}
    />
  )
}

describe('CodeBlockEditor', () => {
  test('does not add a second vertical inset to the line-number gutter for multiline source', () => {
    const { getByRole } = render(
      <CodeBlockEditor
        ariaLabel='Plugin source'
        autoFocus={false}
        language='javascript'
        onChange={() => undefined}
        value={'2\n1\n1'}
      />
    )

    const editor = getByRole('textbox', { name: 'Plugin source' })
    const gutters = editor.querySelector('.cm-gutters')

    // CodeMirror already offsets each gutter line by the content padding.
    // Extra vertical padding here shifts every number below its source line.
    expect(gutters).toHaveStyle({ paddingTop: '0px', paddingBottom: '0px' })
  })

  test('keeps the same editor instance when value and onKeyDown change on rerender', () => {
    const { rerender } = render(editorTree('h'))

    const contentBefore = document.querySelector('.cm-content')
    expect(contentBefore).not.toBeNull()

    rerender(editorTree('hi'))

    const contentAfter = document.querySelector('.cm-content')
    // If the EditorView were torn down and rebuilt, the content node would be
    // replaced and the cursor would reset to the document start, making typed
    // characters pile up at the beginning (text appears right-to-left).
    expect(contentAfter).toBe(contentBefore)
    expect(contentAfter?.textContent).toContain('hi')
  })
})

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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test } from 'vitest'

import { MultiSelect } from '@/components/multi-select'

test('hinted options show the hint in the dropdown and mark their chips without changing the chip name', async () => {
  const user = userEvent.setup()
  render(
    <MultiSelect
      options={[
        { value: 'alias', label: 'alias', hint: 'Redirects to upstream' },
        { value: 'plain', label: 'plain' },
      ]}
      selected={['alias', 'plain']}
      onChange={() => undefined}
      copyChipOnClick
    />
  )

  expect(screen.getByRole('button', { name: 'alias' })).toBeVisible()
  expect(screen.getByTitle('Redirects to upstream')).toHaveAttribute(
    'aria-hidden',
    'true'
  )
  expect(screen.getAllByTitle('Redirects to upstream')).toHaveLength(1)

  await user.click(screen.getByRole('combobox'))
  expect(screen.getByRole('option', { name: /^alias/ })).toHaveTextContent(
    'Redirects to upstream'
  )
  expect(screen.getByRole('option', { name: 'plain' })).not.toHaveTextContent(
    'Redirects to upstream'
  )
})

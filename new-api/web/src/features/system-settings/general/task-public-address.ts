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
export function isValidTaskPublicAddress(value: string): boolean {
  if (value === '') return true
  if (
    value !== value.trim() ||
    !/^https?:\/\//i.test(value) ||
    value.includes('?') ||
    value.includes('#')
  ) {
    return false
  }
  for (const character of value) {
    const codePoint = character.codePointAt(0) ?? 0
    if (codePoint <= 0x1f || codePoint === 0x7f || character === '\\') {
      return false
    }
  }

  try {
    const url = new URL(value)
    const authorityStart = value.indexOf('//') + 2
    const pathStart = value.indexOf('/', authorityStart)
    const authority = value.slice(
      authorityStart,
      pathStart === -1 ? value.length : pathStart
    )
    return (
      (url.protocol === 'https:' || url.protocol === 'http:') &&
      url.hostname.length > 0 &&
      !authority.includes('@') &&
      url.username === '' &&
      url.password === '' &&
      url.search === '' &&
      url.hash === ''
    )
  } catch {
    return false
  }
}

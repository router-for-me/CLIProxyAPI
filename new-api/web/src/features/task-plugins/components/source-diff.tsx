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
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

type SourceDiffProps = { before: string; after: string; className?: string }

type DiffLine = { id: string; kind: 'same' | 'added' | 'removed'; text: string }

function diffLines(before: string, after: string): DiffLine[] {
  const left = before.split('\n')
  const right = after.split('\n')
  const lengths = Array.from({ length: left.length + 1 }, () =>
    Array<number>(right.length + 1).fill(0)
  )
  for (let i = left.length - 1; i >= 0; i -= 1) {
    for (let j = right.length - 1; j >= 0; j -= 1) {
      lengths[i][j] =
        left[i] === right[j]
          ? lengths[i + 1][j + 1] + 1
          : Math.max(lengths[i + 1][j], lengths[i][j + 1])
    }
  }
  const result: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < left.length || j < right.length) {
    if (i < left.length && j < right.length && left[i] === right[j]) {
      result.push({ id: `same-${i}-${j}`, kind: 'same', text: left[i] })
      i += 1
      j += 1
    } else if (
      j < right.length &&
      (i === left.length || lengths[i][j + 1] >= lengths[i + 1][j])
    ) {
      result.push({ id: `added-${i}-${j}`, kind: 'added', text: right[j] })
      j += 1
    } else {
      result.push({ id: `removed-${i}-${j}`, kind: 'removed', text: left[i] })
      i += 1
    }
  }
  return result
}

export function SourceDiff(props: SourceDiffProps) {
  const { t } = useTranslation()
  if (props.before === props.after) {
    return (
      <p
        role='status'
        className='text-muted-foreground rounded-md border px-3 py-6 text-center text-sm'
      >
        {t('No source changes')}
      </p>
    )
  }
  const lines = diffLines(props.before, props.after)
  return (
    <div
      className={cn(
        'max-h-96 overflow-auto rounded-md border font-mono text-xs',
        props.className
      )}
      aria-label={t('Source diff')}
    >
      {lines.map((line) => {
        let prefix = ' '
        let color = ''
        if (line.kind === 'added') {
          prefix = '+'
          color = 'bg-green-500/10 text-green-700 dark:text-green-300'
        } else if (line.kind === 'removed') {
          prefix = '-'
          color = 'bg-red-500/10 text-red-700 dark:text-red-300'
        }
        return (
          <div key={line.id} className={`px-3 whitespace-pre ${color}`}>
            {prefix} {line.text || ' '}
          </div>
        )
      })}
    </div>
  )
}

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
export const staticDataTableClassNames = {
  container: 'overflow-hidden rounded-md border',
  sectionContainer: 'border-border/60 rounded-lg',
  embeddedContainer: 'rounded-none border-0',
  compactTable: 'text-sm',
  compactHeaderRow: 'hover:bg-transparent',
  mutedHeaderRow:
    '[background-color:var(--table-header)] hover:[background-color:var(--table-header-hover)]',
  compactHeaderCell:
    'text-muted-foreground py-2 text-[10px] font-medium tracking-wider uppercase',
  compactHeaderCellRight:
    'text-muted-foreground py-2 text-right text-[10px] font-medium tracking-wider uppercase',
  compactCell: 'py-2.5',
  compactTopCell: 'py-2.5 align-top',
  compactTopNumericCell: 'py-2.5 text-right align-top font-mono',
  compactMutedCell: 'text-muted-foreground py-2.5',
  compactMutedCodeCell: 'text-muted-foreground py-2.5 font-mono',
  compactNumericCell: 'py-2.5 text-right font-mono',
  compactMutedNumericCell: 'text-muted-foreground py-2.5 text-right font-mono',
  topCell: 'py-2 align-top',
  topMutedCell: 'text-muted-foreground py-2 align-top',
  codeCell: 'font-mono text-sm',
  mutedCell: 'text-muted-foreground text-sm',
  mutedCodeCell: 'text-muted-foreground font-mono text-sm',
  topNumericCell: 'py-2 text-right font-mono',
  mediumCell: 'font-medium',
  actionHeaderCell: 'w-auto max-w-none text-right',
  actionCell: 'w-auto max-w-none text-right',
} as const

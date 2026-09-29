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
import { NodeToolbar, Position } from '@xyflow/react'
import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

type ToolbarProps = ComponentProps<typeof NodeToolbar>

export const Toolbar = ({ className, ...props }: ToolbarProps) => (
  <NodeToolbar
    className={cn(
      'bg-background flex items-center gap-1 rounded-sm border p-1.5',
      className
    )}
    position={Position.Bottom}
    {...props}
  />
)

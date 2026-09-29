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
import { Link } from '@tanstack/react-router'
import { ChevronLeft } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { cn } from '@/lib/utils'

import type { SidebarView } from '../types'

type SidebarViewHeaderProps = {
  view: SidebarView
}

/**
 * Header for a nested sidebar view (Vercel / Cloudflare drill-in pattern).
 *
 * Renders only the back affordance — workspace context is conveyed by
 * the nav groups below, not a redundant title row.
 */
export function SidebarViewHeader(props: SidebarViewHeaderProps) {
  const { t } = useTranslation()
  const { isMobile, setOpenMobile } = useSidebar()

  return (
    <SidebarHeader className='border-sidebar-border border-b px-2 py-2'>
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton
            tooltip={t(props.view.parent.label)}
            className={cn(
              'text-muted-foreground hover:text-foreground',
              'gap-1.5 font-medium'
            )}
            render={
              <Link
                to={props.view.parent.to}
                preload={isMobile ? false : undefined}
                onClick={() => setOpenMobile(false)}
              />
            }
          >
            <ChevronLeft className='size-4 shrink-0' />
            <span className='truncate'>{t(props.view.parent.label)}</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarHeader>
  )
}

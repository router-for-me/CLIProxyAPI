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

import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { cn } from '@/lib/utils'

import { defaultTopNavLinks } from '../config/top-nav.config'
import type { TopNavLink } from '../types'

interface PublicNavigationProps {
  /**
   * Custom navigation links
   * If not provided, will use dynamic links from backend or defaults
   */
  links?: TopNavLink[]
  /**
   * Additional className
   */
  className?: string
}

/**
 * Public navigation component that matches Launch UI template styling
 * Used in PublicHeader for desktop navigation
 */
export function PublicNavigation({
  links: providedLinks,
  className,
}: PublicNavigationProps = {}) {
  // Use the same logic as AppHeader: prioritize dynamic links from backend
  const dynamicLinks = useTopNavLinks()
  const defaultLinks = providedLinks || defaultTopNavLinks
  const links = dynamicLinks.length > 0 ? dynamicLinks : defaultLinks

  return (
    <nav className={cn('hidden items-center gap-1 md:flex', className)}>
      {links.map((link, index) => {
        // Handle external links
        if (link.external) {
          return (
            <a
              key={index}
              href={link.href}
              target='_blank'
              rel='noopener noreferrer'
              className={cn(
                'text-muted-foreground hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground inline-flex h-9 w-max items-center justify-center rounded-md bg-transparent px-4 py-2 text-sm font-medium transition-colors focus:outline-none',
                link.disabled && 'pointer-events-none opacity-50'
              )}
            >
              {link.title}
            </a>
          )
        }
        // Handle internal links
        return (
          <Link
            key={index}
            to={link.href}
            className={cn(
              'text-muted-foreground hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground inline-flex h-9 w-max items-center justify-center rounded-md bg-transparent px-4 py-2 text-sm font-medium transition-colors focus:outline-none',
              link.disabled && 'pointer-events-none opacity-50'
            )}
          >
            {link.title}
          </Link>
        )
      })}
    </nav>
  )
}

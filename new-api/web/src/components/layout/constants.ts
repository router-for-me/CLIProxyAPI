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
/**
 * Layout constants and configurations
 */

/**
 * Animation variants for mobile drawer
 */
export const MOBILE_DRAWER_ANIMATION = {
  overlay: {
    hidden: { opacity: 0 },
    visible: { opacity: 1 },
    exit: { opacity: 0 },
  },
  drawer: {
    hidden: { opacity: 0, y: 100 },
    visible: {
      opacity: 1,
      y: 0,
      rotate: 0,
      transition: {
        type: 'spring',
        damping: 15,
        stiffness: 200,
        staggerChildren: 0.03,
      },
    },
    exit: {
      opacity: 0,
      y: 100,
      transition: { duration: 0.1 },
    },
  },
  menuItem: {
    hidden: { opacity: 0 },
    visible: { opacity: 1 },
  },
} as const

/**
 * Mobile drawer configuration
 */
export const MOBILE_DRAWER_CONFIG = {
  overlayTransitionDuration: 0.2,
  drawerClassName:
    'fixed inset-x-0 bottom-3 z-50 mx-auto w-[95%] rounded-xl border border-border bg-background p-4 shadow-lg md:hidden',
  overlayClassName: 'fixed inset-0 z-40 bg-black/50 backdrop-blur-sm',
} as const

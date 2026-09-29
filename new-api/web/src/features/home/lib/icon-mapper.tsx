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
import {
  Zap,
  Shield,
  Globe,
  Code,
  Gauge,
  DollarSign,
  Users,
  HeartHandshake,
  type LucideIcon,
} from 'lucide-react'

/**
 * Map of icon names to Lucide icon components
 */
const ICON_MAP: Record<string, LucideIcon> = {
  Zap,
  Shield,
  Globe,
  Code,
  Gauge,
  DollarSign,
  Users,
  HeartHandshake,
}

/**
 * Get a Lucide icon component by name
 */
export function getFeatureIcon(
  iconName: string,
  className?: string
): React.ReactNode {
  const Icon = ICON_MAP[iconName]
  if (!Icon) {
    // eslint-disable-next-line no-console
    console.warn(`Icon "${iconName}" not found in icon map`)
    return null
  }
  return <Icon className={className} />
}

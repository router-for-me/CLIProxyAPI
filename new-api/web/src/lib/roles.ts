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
import { t } from 'i18next'

export const ROLE = {
  GUEST: 0, // 后续如果需要用到这个角色那就再加，同语先留一下
  USER: 1,
  ADMIN: 10,
  SUPER_ADMIN: 100,
} as const

export type RoleValue = (typeof ROLE)[keyof typeof ROLE]

const DEFAULT_ROLE = ROLE.GUEST

const ROLE_LABEL_KEYS: Record<RoleValue, string> = {
  [ROLE.SUPER_ADMIN]: 'Super Admin',
  [ROLE.ADMIN]: 'Admin',
  [ROLE.USER]: 'User',
  [ROLE.GUEST]: 'Guest',
}

export function getRoleLabelKey(role?: number): string {
  return ROLE_LABEL_KEYS[role as RoleValue] ?? ROLE_LABEL_KEYS[DEFAULT_ROLE]
}

export function getRoleLabel(role?: number): string {
  return t(getRoleLabelKey(role))
}

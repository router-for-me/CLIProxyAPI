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
export type SetupUsageMode = 'external' | 'self' | 'demo'

export interface SetupStatus {
  status: boolean
  root_init: boolean
  database_type: string
  // Some backends also echo mode flags; they are optional here.
  SelfUseModeEnabled?: boolean
  DemoSiteEnabled?: boolean
}

export interface SetupFormValues {
  username: string
  password: string
  confirmPassword: string
  usageMode: SetupUsageMode
}

export interface SetupResponse {
  success: boolean
  message?: string
  data?: SetupStatus
}

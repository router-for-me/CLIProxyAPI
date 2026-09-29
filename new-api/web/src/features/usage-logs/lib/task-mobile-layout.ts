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
interface TaskMobileSummaryField {
  id: string
  label: string
  primaryOnly?: boolean
}

export const TASK_MOBILE_SUMMARY_FIELDS: readonly TaskMobileSummaryField[] = [
  { id: 'submit_time', label: 'Submit Time' },
  { id: 'user', label: 'User', primaryOnly: true },
  { id: 'plugin', label: 'Plugin' },
  { id: 'channel_id', label: 'Channel', primaryOnly: true },
  { id: 'duration', label: 'Duration', primaryOnly: true },
  { id: 'progress', label: 'Progress' },
  { id: 'artifacts', label: 'Artifacts' },
]

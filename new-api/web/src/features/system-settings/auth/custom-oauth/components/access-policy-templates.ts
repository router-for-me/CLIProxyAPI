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
export const ACCESS_POLICY_TEMPLATES = {
  levelAndActive: `{
  "logic": "and",
  "conditions": [
    { "field": "trust_level", "op": "gte", "value": 2 },
    { "field": "active", "op": "eq", "value": true }
  ]
}`,
  orgOrRole: `{
  "logic": "or",
  "conditions": [
    { "field": "org", "op": "eq", "value": "core" },
    { "field": "roles", "op": "contains", "value": "admin" }
  ]
}`,
} as const

export const ACCESS_DENIED_MESSAGE_TEMPLATES = {
  level:
    'Requires level {{required}}; your current level is {{current}} (field: {{field}}).',
  org: 'Access is limited to approved organizations or roles. Organization: {{current.org}}; roles: {{current.roles}}.',
} as const

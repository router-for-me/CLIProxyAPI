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
import { useTranslation } from 'react-i18next'

import { DetailRow } from '../../components/dialogs/log-detail-layout'
import {
  auditFieldLabel,
  isAuditDetailObject,
  type AuditDetailField,
} from '../lib/audit-details'
import { AuditDetailValue } from './audit-detail-value'

export function AuditDetailFields(props: { fields: AuditDetailField[] }) {
  const { t } = useTranslation()
  return props.fields.map((field) => {
    const value = field.value
    if (value === null || value === undefined || value === '') return null
    if (isAuditDetailObject(value) || Array.isArray(value)) {
      const entries = Array.isArray(value)
        ? value.map((item, i) => [String(i + 1), item] as const)
        : Object.entries(value)
      if (!entries.length) return null
      return (
        <div key={field.label} className='min-w-0 space-y-1'>
          <div className='text-muted-foreground text-xs break-all'>
            {field.label}
          </div>
          <div className='space-y-1 border-l pl-2'>
            <AuditDetailFields
              fields={entries.map(([key, item]) => ({
                label: auditFieldLabel(key, t),
                value: item,
              }))}
            />
          </div>
        </div>
      )
    }
    let text = String(value)
    if (typeof value === 'boolean') text = value ? t('Yes') : t('No')
    return (
      <DetailRow
        key={field.label}
        label={field.label}
        value={
          <AuditDetailValue
            label={field.label}
            value={text}
            copyable={field.copyable}
          />
        }
      />
    )
  })
}

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

import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import dayjs from '@/lib/dayjs'

import {
  DetailRow,
  DetailSection,
} from '../../components/dialogs/log-detail-layout'
import type { AuditLog } from '../api'
import { auditFieldLabel, buildAuditDetails } from '../lib/audit-details'
import { AuditDetailFields } from './audit-detail-fields'
import { AuditDetailValue } from './audit-detail-value'

export function AuditLogDetailsDialog(props: { entry: AuditLog }) {
  const { t } = useTranslation()
  const detail = buildAuditDetails(props.entry, t)
  const identifiers = [
    { label: t('Route'), value: props.entry.route },
    { label: t('Request ID'), value: props.entry.request_id },
    { label: t('Token identifier'), value: props.entry.token_ref },
  ].filter((field) => !!field.value)
  const hasOperation =
    detail.actor ||
    detail.target ||
    detail.authentication ||
    detail.fields.length ||
    detail.metadataUnavailable
  const hasRequest = Boolean(
    props.entry.method ||
    props.entry.status ||
    props.entry.ip ||
    props.entry.user_agent ||
    identifiers.length
  )
  return (
    <Dialog
      title={t('Log Details')}
      description={t('View the complete details for this log entry')}
      descriptionClassName='sr-only'
      trigger={
        <Button variant='ghost' size='sm' className='h-7 px-2'>
          {t('Details')}
        </Button>
      }
      contentClassName='min-w-0 sm:max-w-lg max-sm:max-h-(--dialog-available-height) max-sm:w-[calc(100vw-1.5rem)] max-sm:max-w-[calc(100vw-1.5rem)]'
      titleClassName='text-base'
      contentHeight='auto'
      bodyClassName='space-y-3'
    >
      <div className='min-w-0 space-y-1.5'>
        <p className='text-sm leading-relaxed font-medium break-words'>
          {detail.summary}
        </p>
        {detail.operation?.description && (
          <p className='text-muted-foreground text-sm leading-relaxed break-words'>
            {detail.operation.description}
          </p>
        )}
        <div className='flex flex-wrap items-center gap-2 text-xs'>
          <StatusBadge
            label={props.entry.success ? t('Success') : t('Failed')}
            variant={props.entry.success ? 'success' : 'danger'}
            copyable={false}
          />
          {Number.isFinite(props.entry.created_at) && (
            <span className='text-muted-foreground tabular-nums'>
              {dayjs.unix(props.entry.created_at).format('YYYY-MM-DD HH:mm:ss')}
            </span>
          )}
        </div>
      </div>
      {detail.operation && (
        <DetailSection
          label={
            detail.quotaOperation
              ? t('Quota adjustment details')
              : t('Token operation details')
          }
        >
          <AuditDetailFields fields={detail.operation.fields} />
        </DetailSection>
      )}
      {hasOperation && (
        <DetailSection label={t('Operation Audit Info')}>
          {detail.actor && (
            <DetailRow label={t('Operator')} value={detail.actor} />
          )}
          {detail.actorRole && (
            <DetailRow label={t('Role')} value={detail.actorRole} />
          )}
          {!detail.operation && detail.target && (
            <DetailRow label={t('Target')} value={detail.target} />
          )}
          {detail.authentication && (
            <DetailRow
              label={t('Authentication Method')}
              value={detail.authentication}
            />
          )}
          {!detail.operation && <AuditDetailFields fields={detail.fields} />}
          {detail.metadataUnavailable && (
            <p className='text-muted-foreground text-xs'>
              {t('Audit metadata is unavailable')}
            </p>
          )}
        </DetailSection>
      )}
      {hasRequest && (
        <DetailSection label={t('Request')}>
          {props.entry.method && (
            <DetailRow label={t('Method')} value={props.entry.method} mono />
          )}
          {!!props.entry.status && (
            <DetailRow label='HTTP' value={props.entry.status} mono />
          )}
          {props.entry.ip && (
            <DetailRow label='IP' value={props.entry.ip} mono />
          )}
          {props.entry.user_agent && (
            <DetailRow
              label={t('Client')}
              value={
                <AuditDetailValue
                  label={t('User Agent')}
                  value={props.entry.user_agent}
                  mono
                  copyable
                />
              }
            />
          )}
          {identifiers.map((field) => (
            <DetailRow
              key={field.label}
              label={field.label}
              value={
                <AuditDetailValue
                  label={field.label}
                  value={field.value}
                  mono
                  copyable
                />
              }
            />
          ))}
        </DetailSection>
      )}
      {!!Object.keys(detail.extra).length && (
        <DetailSection label={t('Additional information')}>
          <AuditDetailFields
            fields={Object.entries(detail.extra).map(([key, value]) => ({
              label: auditFieldLabel(key, t),
              value,
            }))}
          />
        </DetailSection>
      )}
    </Dialog>
  )
}

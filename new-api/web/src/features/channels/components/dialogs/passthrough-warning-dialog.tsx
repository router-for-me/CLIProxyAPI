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
import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'

export type PassthroughKind = 'body' | 'headers'

type PassthroughWarningDialogProps = {
  open: boolean
  /** Which passthrough option is about to be enabled. */
  kind: PassthroughKind | null
  onOpenChange: (open: boolean) => void
  handleConfirm: () => void
}

/**
 * Shown before a channel passthrough option is switched on. Passthrough
 * bypasses most of the gateway's request processing, so the user must
 * acknowledge which features stop working first.
 */
export function PassthroughWarningDialog(props: PassthroughWarningDialogProps) {
  const { t } = useTranslation()
  const isBody = props.kind !== 'headers'
  const title = isBody
    ? t('Enable request body passthrough?')
    : t('Enable request header passthrough?')
  const useCase = isBody
    ? t(
        'Enable when the client and upstream use the same API format and you need to preserve upstream-specific or new fields the gateway does not yet support. Usually leave this off.'
      )
    : t(
        'Enable when the upstream needs information from clients such as Codex or Claude Code, including User-Agent, client version or session headers. Usually leave this off.'
      )
  const intro = isBody
    ? t(
        'After enabling, the original request body is forwarded to the upstream without gateway rewriting:'
      )
    : t(
        'After enabling, client request headers are forwarded to the upstream, excluding standard authentication headers, cookies and transport headers:'
      )
  const effects = isBody
    ? [
        t('Model redirect is unavailable'),
        t('Parameter override and system prompt are unavailable'),
        t(
          'Format conversion is unavailable; the client must use a format the upstream supports'
        ),
      ]
    : [
        t(
          'Client information such as User-Agent and IP is exposed to the upstream'
        ),
        t(
          'Client headers override the gateway defaults and may cause upstream errors'
        ),
        t('Entries configured in Request Header Override still take priority'),
      ]

  return (
    <ConfirmDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={
        <span className='text-destructive flex items-center gap-2'>
          <AlertTriangle className='size-5 shrink-0' aria-hidden='true' />
          {title}
        </span>
      }
      desc={
        <div className='space-y-3 text-sm'>
          <p>{useCase}</p>
          <p>{intro}</p>
          <ul className='list-disc space-y-1 pl-5'>
            {effects.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
          <p className='text-foreground font-medium'>
            {t('Make sure you understand these effects before enabling.')}
          </p>
        </div>
      }
      confirmText={t('Enable passthrough')}
      destructive
      handleConfirm={props.handleConfirm}
    />
  )
}

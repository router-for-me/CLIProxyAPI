import { useTranslation } from 'react-i18next'

import type { PolicyEvent } from './api'
import { policyLabel } from './policy-label'

export function PolicyDecisionRecord(props: {
  events: PolicyEvent[] | null
  channelNames?: Record<number, string>
}) {
  const { t } = useTranslation()
  return (
    <ol aria-label={t('Request decision flow')} className='space-y-0 text-sm'>
      {(props.events ?? []).map((event, index) => (
        <li
          key={`${event.attempt}:${event.channel_id ?? 0}:${event.decision.action}:${event.decision.reason}:${event.rule ?? ''}`}
          className='border-border relative ml-3 min-w-0 border-l pb-5 pl-6 last:border-transparent last:pb-1'
        >
          <span
            aria-hidden='true'
            className='border-border bg-background absolute -left-3 flex size-6 items-center justify-center rounded-full border text-xs'
          >
            {index + 1}
          </span>
          <p
            className={
              event.decision.action === 'stop'
                ? 'font-medium text-amber-700 dark:text-amber-400'
                : 'font-medium'
            }
          >
            {policyLabel(t, event.decision.reason)}
            {event.channel_id
              ? ` · ${props.channelNames?.[event.channel_id] ?? `#${event.channel_id}`}`
              : ''}
            {event.status && event.decision.action === 'failure'
              ? ` · HTTP ${event.status}`
              : ''}
          </p>
          <p className='text-muted-foreground text-xs break-words'>
            {event.attempt > 0 ? `${t('Attempt')} ${event.attempt} · ` : ''}
            {event.elapsed_ms} ms
            {event.group ? ` · ${event.group}` : ''}
            {event.rule ? ` · ${event.rule}` : ''}
            {event.status ? ` · HTTP ${event.status}` : ''}
          </p>
          <p className='text-muted-foreground text-xs'>
            {t('Source')}: {policyLabel(t, event.decision.source)}
            {event.health ? ` · ${policyLabel(t, event.health)}` : ''}
          </p>
        </li>
      ))}
    </ol>
  )
}

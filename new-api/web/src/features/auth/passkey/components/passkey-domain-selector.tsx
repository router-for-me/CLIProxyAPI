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
import { useQuery } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { statusQueryOptions } from '@/lib/status-query'

import type { PasskeyDomains } from '../assertion'

interface PasskeyDomainSelectorProps {
  domains?: PasskeyDomains | null
  value?: string
  onChange: (rpID: string) => void
  disabled?: boolean
}

export function PasskeyDomainSelector(props: PasskeyDomainSelectorProps) {
  const { t } = useTranslation()
  const id = useId()
  const [expanded, setExpanded] = useState(false)
  const { data: status } = useQuery(statusQueryOptions)
  const configured = Array.isArray(status?.passkey_rp_ids)
    ? status.passkey_rp_ids.filter(
        (value): value is string => typeof value === 'string'
      )
    : []
  const candidates = props.domains?.rpIDs ?? configured
  const available = candidates.filter((rpID) => {
    const domain = rpID.toLowerCase()
    return (
      window.location.hostname === domain ||
      window.location.hostname.endsWith(`.${domain}`)
    )
  })
  if (available.length < 2) return null
  const value = props.value ?? props.domains?.rpID ?? available[0]
  return (
    <div className='space-y-2 text-sm'>
      <Button
        type='button'
        variant='link'
        size='sm'
        className='h-auto p-0'
        disabled={props.disabled}
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
        aria-controls={id}
      >
        {t('Use a Passkey from another website')}
      </Button>
      {expanded && (
        <div id={id} className='space-y-2'>
          <Label htmlFor={`${id}-domain`}>{t('Passkey website domain')}</Label>
          <Select
            value={available.includes(value ?? '') ? value : available[0]}
            onValueChange={(value) => {
              if (value) props.onChange(value)
            }}
            disabled={props.disabled}
          >
            <SelectTrigger id={`${id}-domain`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {available.map((domain) => (
                <SelectItem key={domain} value={domain}>
                  {domain}
                  {domain !== domain.toLowerCase() &&
                    available.some(
                      (other) =>
                        other !== domain &&
                        other.toLowerCase() === domain.toLowerCase()
                    ) && <> · {t('Historical capitalization')}</>}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className='text-muted-foreground'>
            {t(
              'Choose the domain used when this Passkey was created, then try again.'
            )}
          </p>
        </div>
      )}
    </div>
  )
}

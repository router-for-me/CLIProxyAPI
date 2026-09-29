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
import { useMutation } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from '@/components/ui/field'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from '@/components/ui/input-group'
import { Spinner } from '@/components/ui/spinner'

import {
  fetchPluginSourceText,
  normalizePluginSourceUrl,
  PluginSourceFetchError,
} from '../lib/plugin-url'

type PluginUrlImportFieldProps = {
  /** URL text, owned by the dialog so closing it clears this field too. */
  value: string
  onChange: (value: string) => void
  error: string
  onError: (message: string) => void
  onFetched: (source: string) => void
}

export function PluginUrlImportField(props: PluginUrlImportFieldProps) {
  const { t } = useTranslation()
  const importMutation = useMutation({
    meta: { errorToast: false },
    mutationFn: async () => {
      const normalized = normalizePluginSourceUrl(props.value)
      if (!normalized) {
        throw new Error(t('Enter an absolute http(s) URL.'))
      }
      return fetchPluginSourceText(normalized)
    },
    // The fetched text only fills the source field. Uploading stays an explicit
    // administrator action so the source and the risk warning are reviewed
    // exactly as they are for a manual paste.
    onSuccess: (text) => {
      props.onError('')
      props.onFetched(text)
    },
    onError: (error) => {
      if (!(error instanceof PluginSourceFetchError)) {
        props.onError(error.message)
        return
      }
      if (error.reason === 'too_large') {
        props.onError(t('Plugin source exceeds the 8 MiB limit.'))
        return
      }
      if (error.reason === 'not_found') {
        props.onError(
          t(
            'The URL returned HTTP {{status}}. Check the address, or download the file and paste its source below.',
            { status: error.status ?? 0 }
          )
        )
        return
      }
      props.onError(
        t(
          'Could not fetch this URL from the browser. The host may block cross-origin requests or be unreachable. Download the file and paste its source below.'
        )
      )
    },
  })

  return (
    <Field data-invalid={props.error ? true : undefined}>
      <FieldLabel htmlFor='task-plugin-url'>{t('Import from URL')}</FieldLabel>
      <InputGroup>
        <InputGroupInput
          id='task-plugin-url'
          type='url'
          inputMode='url'
          value={props.value}
          placeholder='https://github.com/owner/repo/blob/main/plugin.js'
          aria-describedby='task-plugin-url-hint'
          aria-invalid={props.error ? true : undefined}
          onChange={(event) => {
            props.onChange(event.target.value)
            props.onError('')
          }}
          onKeyDown={(event) => {
            if (event.key !== 'Enter') return
            event.preventDefault()
            if (props.value.trim()) importMutation.mutate()
          }}
        />
        <InputGroupAddon align='inline-end'>
          <InputGroupButton
            variant='outline'
            disabled={!props.value.trim() || importMutation.isPending}
            onClick={() => importMutation.mutate()}
          >
            {importMutation.isPending ? (
              <Spinner aria-hidden='true' className='size-3.5' />
            ) : (
              <Download aria-hidden='true' />
            )}
            {importMutation.isPending ? t('Fetching...') : t('Fetch')}
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      <FieldDescription id='task-plugin-url-hint'>
        {t(
          'Fetched in your browser and placed in the source field below for review. GitHub and gist page URLs are rewritten to their raw URL automatically.'
        )}
      </FieldDescription>
      {props.error ? <FieldError>{props.error}</FieldError> : null}
    </Field>
  )
}

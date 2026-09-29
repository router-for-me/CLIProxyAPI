/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import { useMutation } from '@tanstack/react-query'
import { Play } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CodeBlock, CodeBlockEditor } from '@/components/ai-elements/code-block'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'

import { dryRunTaskPlugin } from '../api'

const hooks = [
  'resolveRequest',
  'buildSubmitRequest',
  'parseSubmitResponse',
  'extractUsage',
  'extractUsageOnSubmit',
  'extractUsageOnComplete',
  'buildQueryRequest',
  'parseTaskResult',
  'buildBatchQueryRequest',
  'parseBatchResult',
  'buildContentRequest',
  'renderers.openai_video',
]

export function PluginSandbox(props: { pluginKey: string }) {
  const { t } = useTranslation()
  const [hook, setHook] = useState('buildSubmitRequest')
  const [args, setArgs] = useState('[{}]')
  const [output, setOutput] = useState('')
  const mutation = useMutation({
    meta: { errorToast: false },
    mutationFn: async () => {
      const parsed = JSON.parse(args) as unknown
      if (!Array.isArray(parsed)) {
        throw new Error(t('Arguments must be a JSON array'))
      }
      const memberSeparator = hook.indexOf('.')
      return dryRunTaskPlugin(props.pluginKey, {
        hook: memberSeparator < 0 ? hook : hook.slice(0, memberSeparator),
        member:
          memberSeparator < 0 ? undefined : hook.slice(memberSeparator + 1),
        args: parsed,
      })
    },
    onSuccess: (value) => setOutput(JSON.stringify(value, null, 2)),
    onError: (error) =>
      setOutput(JSON.stringify({ error: error.message }, null, 2)),
  })

  return (
    <div className='flex flex-col gap-4'>
      <Combobox
        options={hooks.map((item) => ({ value: item, label: item }))}
        value={hook}
        onValueChange={(value) => setHook(value ?? '')}
        aria-label={t('Hook')}
      />
      <CodeBlockEditor
        ariaLabel={t('Arguments JSON')}
        language='json'
        onChange={setArgs}
        rows={12}
        title={t('Arguments JSON')}
        value={args}
      />
      <Button disabled={mutation.isPending} onClick={() => mutation.mutate()}>
        <Play aria-hidden='true' />
        {mutation.isPending ? t('Running dry run') : t('Run dry run')}
      </Button>
      {output && (
        <CodeBlock code={output} language='json' title={t('Dry run result')} />
      )}
    </div>
  )
}

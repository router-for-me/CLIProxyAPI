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
import { Database, HardDrive, Server } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'

import type { SetupStatus } from '../types'

interface DatabaseStepProps {
  status?: SetupStatus
}

const DATABASE_META: Record<
  string,
  {
    label: string
    descriptionKey: string
    variant: 'info' | 'success' | 'warning'
  }
> = {
  sqlite: {
    label: 'SQLite',
    descriptionKey:
      'SQLite stores all data in a single file. Make sure that file is persisted when running in containers.',
    variant: 'warning',
  },
  mysql: {
    label: 'MySQL',
    descriptionKey:
      'MySQL is a production-ready relational database. Keep your credentials secure.',
    variant: 'success',
  },
  postgres: {
    label: 'PostgreSQL',
    descriptionKey:
      'PostgreSQL offers advanced reliability and data integrity for production workloads.',
    variant: 'success',
  },
}

function resolveDatabaseMeta(type?: string) {
  if (!type) return null
  const normalized = type.toLowerCase()
  return (
    DATABASE_META[normalized] ?? {
      label: type,
      descriptionKey: 'Custom database driver detected.',
      variant: 'info' as const,
    }
  )
}

export function DatabaseStep({ status }: DatabaseStepProps) {
  const { t } = useTranslation()
  const meta = resolveDatabaseMeta(status?.database_type)
  const electronApi =
    typeof window !== 'undefined'
      ? ((window as unknown as Record<string, unknown>)?.electron as
          | Record<string, unknown>
          | undefined)
      : undefined
  const isElectron = Boolean(electronApi?.isElectron)
  const electronDataDir = electronApi?.dataDir as string | undefined

  return (
    <div className='space-y-4'>
      <div className='bg-card flex items-center justify-between rounded-lg border p-4'>
        <div className='space-y-1'>
          <p className='text-muted-foreground text-sm font-medium'>
            {t('Detected database')}
          </p>
          <p className='text-foreground text-base font-semibold'>
            {meta?.label ?? t('Unknown')}
          </p>
          <p className='text-muted-foreground text-sm'>
            {t(
              meta?.descriptionKey ??
                'The setup wizard will use this database during initialization.'
            )}
          </p>
        </div>
        <StatusBadge
          label={meta?.label ?? t('Unknown')}
          variant={meta?.variant ?? 'info'}
          className='cursor-default'
          copyable={false}
          icon={Database}
        />
      </div>

      {status?.database_type === 'sqlite' && (
        <Alert className='border-amber-200 bg-amber-50 dark:border-amber-900/60 dark:bg-amber-950/40'>
          <AlertTitle className='flex items-center gap-2'>
            <HardDrive className='size-4 text-amber-500' />
            {t('Persist your data file')}
          </AlertTitle>
          <AlertDescription>
            <p>
              {t(
                'When running in containers or ephemeral environments, ensure the SQLite file is mapped to persistent storage to avoid data loss on restart.'
              )}
            </p>
            {isElectron && electronDataDir && (
              <p className='mt-3 rounded-md bg-amber-100/70 px-3 py-2 font-mono text-xs text-amber-800 dark:bg-amber-900/30 dark:text-amber-200'>
                {t('Data directory:')} {electronDataDir}
              </p>
            )}
            {isElectron && !electronDataDir && (
              <p className='text-muted-foreground mt-3 text-xs'>
                {t(
                  'Data is stored locally on this device. Use system backups to keep a safe copy.'
                )}
              </p>
            )}
          </AlertDescription>
        </Alert>
      )}

      {status?.database_type === 'mysql' && (
        <Alert className='border-emerald-200 bg-emerald-50 dark:border-emerald-900/60 dark:bg-emerald-950/40'>
          <AlertTitle className='flex items-center gap-2'>
            <Server className='size-4 text-emerald-500' />
            {t('MySQL detected')}
          </AlertTitle>
          <AlertDescription>
            {t(
              'MySQL is production ready. Ensure automated backups and a dedicated user with the minimal required privileges are configured.'
            )}
          </AlertDescription>
        </Alert>
      )}

      {status?.database_type === 'postgres' && (
        <Alert className='border-sky-200 bg-sky-50 dark:border-sky-900/60 dark:bg-sky-950/40'>
          <AlertTitle className='flex items-center gap-2'>
            <Server className='size-4 text-sky-500' />
            {t('PostgreSQL detected')}
          </AlertTitle>
          <AlertDescription>
            {t(
              'PostgreSQL offers strong reliability guarantees. Double check your maintenance window and retention policies before going live.'
            )}
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

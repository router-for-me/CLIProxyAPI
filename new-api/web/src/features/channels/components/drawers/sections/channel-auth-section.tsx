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
import { KeyRound } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

type ChannelAuthSectionProps = {
  children: ReactNode
}

export function ChannelAuthSection(props: ChannelAuthSectionProps) {
  const { t } = useTranslation()

  return (
    <div className='border-border/60 flex flex-col gap-3 border-t pt-4'>
      <div className='flex items-center gap-2'>
        <KeyRound
          className='text-muted-foreground h-3.5 w-3.5'
          aria-hidden='true'
        />
        <h4 className='text-muted-foreground text-xs font-medium tracking-wide uppercase'>
          {t('Authentication')}
        </h4>
      </div>
      {props.children}
    </div>
  )
}

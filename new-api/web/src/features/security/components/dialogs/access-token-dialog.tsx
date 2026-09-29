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

import { CopyButton } from '@/components/copy-button'
import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

export function AccessTokenDialog(props: {
  token: string
  onClose: () => void
}) {
  const { t } = useTranslation()
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('Access Token')}
      description={t(
        "Save this token now. You won't be able to view it again after closing this dialog."
      )}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      footer={<Button onClick={props.onClose}>{t('Close')}</Button>}
    >
      <div className='space-y-2 py-2'>
        <Label htmlFor='generated-access-token'>{t('Token')}</Label>
        <div className='flex gap-2'>
          <Input
            id='generated-access-token'
            value={props.token}
            readOnly
            autoComplete='off'
            className='font-mono text-xs'
          />
          <CopyButton
            value={props.token}
            variant='outline'
            tooltip={t('Copy token')}
            aria-label={t('Copy token')}
          />
        </div>
      </div>
    </Dialog>
  )
}

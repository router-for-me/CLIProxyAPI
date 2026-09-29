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
import { Plus } from 'lucide-react'
import { type ChangeEvent, useRef, type SetStateAction, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'

import { SettingsSwitchField } from '../components/settings-form-layout'

export interface WaffoSettingsValues {
  WaffoEnabled: boolean
  WaffoApiKey: string
  WaffoPrivateKey: string
  WaffoPublicCert: string
  WaffoSandboxPublicCert: string
  WaffoSandboxApiKey: string
  WaffoSandboxPrivateKey: string
  WaffoSandbox: boolean
  WaffoMerchantId: string
  WaffoCurrency: string
  WaffoUnitPrice: number
  WaffoMinTopUp: number
  WaffoNotifyUrl: string
  WaffoReturnUrl: string
  WaffoPayMethods: string
}

export interface PayMethod {
  name: string
  icon: string
  payMethodType: string
  payMethodName: string
}

type WaffoFieldValues = Omit<WaffoSettingsValues, 'WaffoPayMethods'>

interface Props {
  values: WaffoSettingsValues
  onValueChange: <K extends keyof WaffoFieldValues>(
    key: K,
    value: WaffoFieldValues[K]
  ) => void
  payMethods: PayMethod[]
  onPayMethodsChange: (value: SetStateAction<PayMethod[]>) => void
}

export function WaffoSettingsSection({
  values,
  onValueChange,
  payMethods,
  onPayMethodsChange,
}: Props) {
  const { t } = useTranslation()
  const iconFileInputRef = useRef<HTMLInputElement | null>(null)
  const [methodDialogOpen, setMethodDialogOpen] = useState(false)
  const [editingIdx, setEditingIdx] = useState(-1)
  const [methodForm, setMethodForm] = useState<PayMethod>({
    name: '',
    icon: '',
    payMethodType: '',
    payMethodName: '',
  })

  const openAdd = () => {
    setEditingIdx(-1)
    setMethodForm({ name: '', icon: '', payMethodType: '', payMethodName: '' })
    setMethodDialogOpen(true)
  }

  const openEdit = (idx: number) => {
    setEditingIdx(idx)
    setMethodForm({ ...payMethods[idx] })
    setMethodDialogOpen(true)
  }

  const saveMethod = () => {
    if (!methodForm.name.trim()) {
      return toast.error(t('Payment method name is required'))
    }
    if (editingIdx === -1) {
      onPayMethodsChange((prev) => [...prev, methodForm])
    } else {
      onPayMethodsChange((prev) =>
        prev.map((m, i) => (i === editingIdx ? methodForm : m))
      )
    }
    setMethodDialogOpen(false)
  }

  const handleIconFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]

    if (!file) {
      return
    }

    const maxIconSize = 100 * 1024

    if (file.size > maxIconSize) {
      toast.error(t('Icon file must be 100 KB or smaller'))
      event.target.value = ''
      return
    }

    const reader = new FileReader()
    reader.addEventListener('load', () => {
      setMethodForm((previous) => ({
        ...previous,
        icon: typeof reader.result === 'string' ? reader.result : '',
      }))
    })
    reader.readAsDataURL(file)
    event.target.value = ''
  }

  return (
    <>
      <div className='space-y-4 pt-4'>
        <div>
          <h3 className='text-lg font-medium'>
            {t('Waffo Aggregator Gateway')}
          </h3>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Payment aggregator mode — onboard with your own registered company (offshore entity). Built for Enterprise.'
            )}
          </p>
        </div>
        <Alert>
          <AlertDescription className='text-xs'>
            {t(
              'Obtain the API key, merchant ID, and RSA key pair from the Waffo dashboard, and configure the callback URL.'
            )}
          </AlertDescription>
        </Alert>

        <div className='grid gap-4 sm:grid-cols-2'>
          <SettingsSwitchField
            checked={values.WaffoEnabled}
            onCheckedChange={(v) => onValueChange('WaffoEnabled', v)}
            label={t('Enable Waffo')}
            className='py-0'
          />
          <SettingsSwitchField
            checked={values.WaffoSandbox}
            onCheckedChange={(v) => onValueChange('WaffoSandbox', v)}
            label={t('Sandbox mode')}
            className='py-0'
          />
        </div>

        <div className='grid grid-cols-2 gap-4'>
          <div className='grid gap-1.5'>
            <Label>{t('API Key (Production)')}</Label>
            <Input
              type='password'
              value={values.WaffoApiKey}
              onChange={(event) =>
                onValueChange('WaffoApiKey', event.target.value)
              }
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('API Key (Sandbox)')}</Label>
            <Input
              type='password'
              value={values.WaffoSandboxApiKey}
              onChange={(event) =>
                onValueChange('WaffoSandboxApiKey', event.target.value)
              }
            />
          </div>
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('Merchant ID')}</Label>
          <Input
            value={values.WaffoMerchantId}
            onChange={(event) =>
              onValueChange('WaffoMerchantId', event.target.value)
            }
          />
        </div>

        <div className='grid grid-cols-2 gap-4'>
          <div className='grid gap-1.5'>
            <Label>{t('RSA Private Key (Production)')}</Label>
            <Textarea
              rows={3}
              value={values.WaffoPrivateKey}
              onChange={(event) =>
                onValueChange('WaffoPrivateKey', event.target.value)
              }
              className='font-mono text-xs'
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('RSA Private Key (Sandbox)')}</Label>
            <Textarea
              rows={3}
              value={values.WaffoSandboxPrivateKey}
              onChange={(event) =>
                onValueChange('WaffoSandboxPrivateKey', event.target.value)
              }
              className='font-mono text-xs'
            />
          </div>
        </div>

        <div className='grid grid-cols-2 gap-4'>
          <div className='grid gap-1.5'>
            <Label>{t('Waffo Public Key (Production)')}</Label>
            <Textarea
              rows={3}
              value={values.WaffoPublicCert}
              onChange={(event) =>
                onValueChange('WaffoPublicCert', event.target.value)
              }
              className='font-mono text-xs'
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Waffo Public Key (Sandbox)')}</Label>
            <Textarea
              rows={3}
              value={values.WaffoSandboxPublicCert}
              onChange={(event) =>
                onValueChange('WaffoSandboxPublicCert', event.target.value)
              }
              className='font-mono text-xs'
            />
          </div>
        </div>

        <div className='grid grid-cols-3 gap-4'>
          <div className='grid gap-1.5'>
            <Label>{t('Currency')}</Label>
            <Input value={values.WaffoCurrency} disabled />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Unit price (USD)')}</Label>
            <Input
              type='number'
              step={0.1}
              min={0}
              value={values.WaffoUnitPrice}
              onChange={(event) =>
                onValueChange(
                  'WaffoUnitPrice',
                  event.target.value === '' ? 0 : event.target.valueAsNumber
                )
              }
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Minimum top-up quantity')}</Label>
            <Input
              type='number'
              min={1}
              value={values.WaffoMinTopUp}
              onChange={(event) =>
                onValueChange(
                  'WaffoMinTopUp',
                  event.target.value === '' ? 1 : event.target.valueAsNumber
                )
              }
            />
          </div>
        </div>

        <div className='grid grid-cols-2 gap-4'>
          <div className='grid gap-1.5'>
            <Label>{t('Callback notification URL')}</Label>
            <Input
              placeholder='https://example.com/api/waffo/webhook'
              value={values.WaffoNotifyUrl}
              onChange={(event) =>
                onValueChange('WaffoNotifyUrl', event.target.value)
              }
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Payment return URL')}</Label>
            <Input
              placeholder='https://example.com/wallet'
              value={values.WaffoReturnUrl}
              onChange={(event) =>
                onValueChange('WaffoReturnUrl', event.target.value)
              }
            />
          </div>
        </div>

        <Separator />

        <div className='flex items-center justify-between'>
          <h4 className='font-medium'>{t('Payment Methods')}</h4>
          <Button type='button' variant='outline' size='sm' onClick={openAdd}>
            <Plus className='mr-1 h-3 w-3' />
            {t('Add payment method')}
          </Button>
        </div>

        <StaticDataTable
          data={payMethods}
          emptyClassName='text-muted-foreground py-8'
          emptyContent={t('No payment methods configured')}
          columns={[
            {
              id: 'name',
              header: t('Display name'),
              cell: (m) => m.name,
            },
            {
              id: 'icon',
              header: t('Icon'),
              cell: (m) =>
                m.icon ? (
                  <img
                    src={m.icon}
                    alt={m.name}
                    className='h-6 w-6 rounded object-contain'
                  />
                ) : (
                  <span className='text-muted-foreground'>-</span>
                ),
            },
            {
              id: 'type',
              header: t('Payment method type'),
              cell: (m) => m.payMethodType || '-',
            },
            {
              id: 'method',
              header: t('Payment method name'),
              cell: (m) => m.payMethodName || '-',
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (_m, idx) => (
                <StaticRowActions
                  editLabel={t('Edit')}
                  deleteLabel={t('Delete')}
                  menuLabel={t('Open menu')}
                  onEdit={() => openEdit(idx)}
                  onDelete={() =>
                    onPayMethodsChange((prev) =>
                      prev.filter((_, i) => i !== idx)
                    )
                  }
                />
              ),
            },
          ]}
        />
      </div>

      <Dialog
        open={methodDialogOpen}
        onOpenChange={setMethodDialogOpen}
        title={
          editingIdx === -1 ? t('Add payment method') : t('Edit payment method')
        }
        contentHeight='auto'
        bodyClassName='space-y-4'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => setMethodDialogOpen(false)}
            >
              {t('Cancel')}
            </Button>
            <Button type='button' onClick={saveMethod}>
              {t('Confirm')}
            </Button>
          </>
        }
      >
        <div className='space-y-3'>
          <div className='grid gap-1.5'>
            <Label required>{t('Display name')}</Label>
            <Input
              value={methodForm.name}
              onChange={(e) =>
                setMethodForm((p) => ({ ...p, name: e.target.value }))
              }
            />
          </div>
          <div className='grid gap-2'>
            <Label>{t('Icon')}</Label>
            <div className='flex items-center gap-3'>
              {methodForm.icon ? (
                <img
                  src={methodForm.icon}
                  alt={methodForm.name || t('Icon')}
                  className='h-10 w-10 rounded border object-contain p-1'
                />
              ) : (
                <div className='bg-muted text-muted-foreground flex h-10 w-10 items-center justify-center rounded border text-xs'>
                  {t('Icon')}
                </div>
              )}
              <input
                ref={iconFileInputRef}
                type='file'
                accept='image/png,image/jpeg,image/svg+xml,image/webp'
                className='hidden'
                onChange={handleIconFileChange}
              />
              <Button
                type='button'
                variant='outline'
                onClick={() => iconFileInputRef.current?.click()}
              >
                {t('Upload')}
              </Button>
              {methodForm.icon ? (
                <Button
                  type='button'
                  variant='outline'
                  onClick={() =>
                    setMethodForm((previous) => ({
                      ...previous,
                      icon: '',
                    }))
                  }
                >
                  {t('Clear')}
                </Button>
              ) : null}
            </div>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Supports PNG, JPG, SVG, or WebP. Recommended size: 128×128 or smaller.'
              )}
            </p>
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Payment method type')}</Label>
            <Input
              value={methodForm.payMethodType}
              onChange={(e) =>
                setMethodForm((p) => ({
                  ...p,
                  payMethodType: e.target.value,
                }))
              }
              placeholder='CREDITCARD,DEBITCARD'
            />
          </div>
          <div className='grid gap-1.5'>
            <Label>{t('Payment method name')}</Label>
            <Input
              value={methodForm.payMethodName}
              onChange={(e) =>
                setMethodForm((p) => ({
                  ...p,
                  payMethodName: e.target.value,
                }))
              }
            />
          </div>
        </div>
      </Dialog>
    </>
  )
}

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
import { zodResolver } from '@hookform/resolvers/zod'
import { Plus, Trash2, Save } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { Dialog } from '@/components/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import { SettingsSwitchField } from '../components/settings-form-layout'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

type FAQ = {
  id: number
  question: string
  answer: string
}

type FAQSectionProps = {
  enabled: boolean
  data: string
}

const faqSchema = z.object({
  question: z
    .string()
    .min(1, 'Question is required')
    .max(200, 'Question must be less than 200 characters'),
  answer: z
    .string()
    .min(1, 'Answer is required')
    .max(1000, 'Answer must be less than 1000 characters'),
})

type FAQFormValues = z.infer<typeof faqSchema>

const FAQ_FORM_ID = 'faq-form'

export function FAQSection({ enabled, data }: FAQSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const [faqList, setFaqList] = useState<FAQ[]>([])
  const [isEnabled, setIsEnabled] = useState(enabled)
  const [hasChanges, setHasChanges] = useState(false)
  const [selectedIds, setSelectedIds] = useState<number[]>([])
  const [showDialog, setShowDialog] = useState(false)
  const [showDeleteDialog, setShowDeleteDialog] = useState(false)
  const [editingFaq, setEditingFaq] = useState<FAQ | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<'single' | 'batch'>('single')

  const form = useForm<FAQFormValues>({
    resolver: zodResolver(faqSchema),
    defaultValues: {
      question: '',
      answer: '',
    },
  })

  useEffect(() => {
    try {
      const parsed = JSON.parse(data || '[]')
      if (Array.isArray(parsed)) {
        setFaqList(
          parsed.map((item, idx) => ({
            ...item,
            id: item.id || idx + 1,
          }))
        )
      }
    } catch {
      setFaqList([])
    }
  }, [data])

  useEffect(() => {
    setIsEnabled(enabled)
  }, [enabled])

  const handleToggleEnabled = async (checked: boolean) => {
    try {
      await updateOption.mutateAsync({
        key: 'console_setting.faq_enabled',
        value: checked,
      })
      setIsEnabled(checked)
      toast.success(t('Setting saved'))
    } catch (error) {
      handleServerError(error, t('Failed to update setting'))
    }
  }

  const handleAdd = () => {
    setEditingFaq(null)
    form.reset({
      question: '',
      answer: '',
    })
    setShowDialog(true)
  }

  const handleEdit = (faq: FAQ) => {
    setEditingFaq(faq)
    form.reset({
      question: faq.question,
      answer: faq.answer,
    })
    setShowDialog(true)
  }

  const handleDelete = (faq: FAQ) => {
    setEditingFaq(faq)
    setDeleteTarget('single')
    setShowDeleteDialog(true)
  }

  const handleBatchDelete = () => {
    if (selectedIds.length === 0) {
      toast.error(t('Please select items to delete'))
      return
    }
    setDeleteTarget('batch')
    setShowDeleteDialog(true)
  }

  const confirmDelete = () => {
    if (deleteTarget === 'single' && editingFaq) {
      setFaqList((prev) => prev.filter((item) => item.id !== editingFaq.id))
      setHasChanges(true)
      toast.success(t('FAQ deleted. Click "Save Settings" to apply.'))
    } else if (deleteTarget === 'batch') {
      setFaqList((prev) =>
        prev.filter((item) => !selectedIds.includes(item.id))
      )
      setSelectedIds([])
      setHasChanges(true)
      toast.success(
        t('{{count}} FAQs deleted. Click "Save Settings" to apply.', {
          count: selectedIds.length,
        })
      )
    }
    setShowDeleteDialog(false)
    setEditingFaq(null)
  }

  const handleSubmitForm = (values: FAQFormValues) => {
    if (editingFaq) {
      setFaqList((prev) =>
        prev.map((item) =>
          item.id === editingFaq.id ? { ...item, ...values } : item
        )
      )
      toast.success(t('FAQ updated. Click "Save Settings" to apply.'))
    } else {
      const newId = Math.max(...faqList.map((item) => item.id), 0) + 1
      setFaqList((prev) => [...prev, { id: newId, ...values }])
      toast.success(t('FAQ added. Click "Save Settings" to apply.'))
    }
    setHasChanges(true)
    setShowDialog(false)
  }

  const handleSaveAll = async () => {
    try {
      await updateOption.mutateAsync({
        key: 'console_setting.faq',
        value: JSON.stringify(faqList),
      })
      setHasChanges(false)
      toast.success(t('FAQ saved successfully'))
    } catch (error) {
      handleServerError(error, t('Failed to save FAQ'))
    }
  }

  const toggleSelectAll = (checked: boolean) => {
    setSelectedIds(checked ? faqList.map((item) => item.id) : [])
  }

  const toggleSelectOne = (id: number, checked: boolean) => {
    setSelectedIds((prev) =>
      checked ? [...prev, id] : prev.filter((item) => item !== id)
    )
  }

  return (
    <SettingsSection title={t('FAQ')}>
      <div className='space-y-4'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <div className='flex flex-wrap items-center gap-2'>
            <Button onClick={handleAdd} size='sm'>
              <Plus className='mr-2 h-4 w-4' />
              {t('Add FAQ')}
            </Button>
            <Button
              onClick={handleBatchDelete}
              size='sm'
              variant='destructive'
              disabled={selectedIds.length === 0}
            >
              <Trash2 className='mr-2 h-4 w-4' />
              {t('Delete (')}
              {selectedIds.length})
            </Button>
            <Button
              onClick={handleSaveAll}
              size='sm'
              variant='secondary'
              disabled={!hasChanges || updateOption.isPending}
            >
              <Save className='mr-2 h-4 w-4' />
              {updateOption.isPending ? t('Saving...') : t('Save Settings')}
            </Button>
          </div>
          <SettingsSwitchField
            checked={isEnabled}
            onCheckedChange={handleToggleEnabled}
            label={t('Enabled')}
            className='py-0'
          />
        </div>

        <StaticDataTable
          data={faqList}
          getRowKey={(faq) => faq.id}
          emptyContent={t('No FAQ entries yet. Click "Add FAQ" to create one.')}
          columns={[
            {
              id: 'select',
              header: (
                <Checkbox
                  checked={
                    selectedIds.length === faqList.length && faqList.length > 0
                  }
                  onCheckedChange={toggleSelectAll}
                />
              ),
              className: 'w-12',
              cell: (faq) => (
                <Checkbox
                  checked={selectedIds.includes(faq.id)}
                  onCheckedChange={(checked) =>
                    toggleSelectOne(faq.id, checked as boolean)
                  }
                />
              ),
            },
            {
              id: 'question',
              header: t('Question'),
              cellClassName: 'max-w-xs truncate font-medium',
              cell: (faq) => faq.question,
            },
            {
              id: 'answer',
              header: t('Answer'),
              cellClassName: 'text-muted-foreground max-w-md truncate',
              cell: (faq) => faq.answer,
            },
            {
              id: 'actions',
              header: t('Actions'),
              cell: (faq) => (
                <StaticRowActions
                  editLabel={t('Edit')}
                  deleteLabel={t('Delete')}
                  menuLabel={t('Open menu')}
                  onEdit={() => handleEdit(faq)}
                  onDelete={() => handleDelete(faq)}
                />
              ),
            },
          ]}
        />
      </div>

      <Dialog
        open={showDialog}
        onOpenChange={setShowDialog}
        title={editingFaq ? t('Edit FAQ') : t('Add FAQ')}
        description={t('Create or update frequently asked questions for users')}
        contentClassName='max-w-2xl'
        contentHeight='auto'
        bodyClassName='space-y-4'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => setShowDialog(false)}
            >
              {t('Cancel')}
            </Button>
            <Button type='submit' form={FAQ_FORM_ID}>
              {editingFaq ? t('Update') : t('Add')}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form
            id={FAQ_FORM_ID}
            onSubmit={form.handleSubmit(handleSubmitForm)}
            className='space-y-4'
          >
            <FormField
              control={form.control}
              name='question'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Question')}</FormLabel>
                  <FormControl>
                    <Input
                      placeholder={t('How to reset my quota?')}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Maximum 200 characters')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='answer'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Answer')}</FormLabel>
                  <FormControl>
                    <Textarea
                      placeholder={t(
                        'Visit Settings → General and adjust quota options...'
                      )}
                      rows={8}
                      {...field}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Maximum 1000 characters. Supports Markdown and HTML.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </form>
        </Form>
      </Dialog>

      <AlertDialog open={showDeleteDialog} onOpenChange={setShowDeleteDialog}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('Are you sure?')}</AlertDialogTitle>
            <AlertDialogDescription>
              {deleteTarget === 'single'
                ? t('This FAQ entry will be removed from the list.')
                : t('{{count}} FAQ entries will be removed from the list.', {
                    count: selectedIds.length,
                  })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('Cancel')}</AlertDialogCancel>
            <AlertDialogAction variant='destructive' onClick={confirmDelete}>
              {t('Delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsSection>
  )
}

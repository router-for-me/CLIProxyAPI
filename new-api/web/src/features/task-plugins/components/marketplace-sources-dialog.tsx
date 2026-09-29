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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { handleServerError } from '@/lib/handle-server-error'

import { listMarketplaceSources, updateMarketplaceSources } from '../api'
import { isDefaultMarketplaceSource } from '../lib/marketplace'
import type { MarketplaceSource } from '../types'

type MarketplaceSourcesDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * Rows carry a client-only identity so React keys survive edits and removals.
 * Keying by position would hand a deleted row's input state to its successor,
 * and an index URL is not unique until the administrator finishes typing it.
 */
type DraftRow = MarketplaceSource & { rowId: string }

export function MarketplaceSourcesDialog(props: MarketplaceSourcesDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<DraftRow[]>([])
  const nextRowId = useRef(0)
  const makeRowId = () => {
    nextRowId.current += 1
    return `row-${nextRowId.current}`
  }
  const sourcesQuery = useQuery({
    queryKey: ['task-plugin-marketplace-sources'],
    queryFn: listMarketplaceSources,
    enabled: props.open,
  })

  // The dialog edits a local copy so a half-typed row is never pushed to the
  // server; it is re-seeded whenever the dialog opens with fresh server data.
  useEffect(() => {
    if (props.open && sourcesQuery.data) {
      setDraft(
        sourcesQuery.data.map((source) => ({ ...source, rowId: makeRowId() }))
      )
    }
  }, [props.open, sourcesQuery.data])

  const saveMutation = useMutation({
    mutationFn: updateMarketplaceSources,
    onSuccess: (saved) => {
      queryClient.setQueryData(['task-plugin-marketplace-sources'], saved)
      queryClient.invalidateQueries({ queryKey: ['task-plugin-marketplace'] })
      toast.success(t('Marketplace sources updated'))
      props.onOpenChange(false)
    },
    onError: (error) => handleServerError(error),
  })

  const updateRow = (index: number, patch: Partial<MarketplaceSource>) => {
    setDraft((rows) =>
      rows.map((row, position) =>
        position === index ? { ...row, ...patch } : row
      )
    )
  }

  const invalidRow = draft.some(
    (row) => !row.name.trim() || !row.index_url.trim()
  )

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='max-w-2xl'>
        <DialogHeader>
          <DialogTitle>{t('Marketplace sources')}</DialogTitle>
          <DialogDescription>
            {t(
              'Each source serves an index.json listing installable plugins. Indexes are fetched by your browser; the gateway makes no outbound requests.'
            )}
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-3'>
          {draft.length === 0 && (
            <p className='text-muted-foreground text-sm'>
              {t('No marketplace sources configured.')}
            </p>
          )}
          {draft.map((row, index) => (
            <div key={row.rowId} className='space-y-2 rounded-md border p-3'>
              <div className='flex items-center justify-between gap-2'>
                <Label htmlFor={`marketplace-source-name-${row.rowId}`}>
                  {t('Source name')}
                </Label>
                <div className='flex items-center gap-2'>
                  {isDefaultMarketplaceSource(row.index_url) ? (
                    <Badge variant='secondary'>{t('Official')}</Badge>
                  ) : (
                    <Badge variant='destructive'>
                      {t('Third-party — use at your own risk')}
                    </Badge>
                  )}
                  <Button
                    variant='ghost'
                    size='icon-sm'
                    aria-label={t('Remove source {{name}}', {
                      name: row.name || row.index_url,
                    })}
                    onClick={() =>
                      setDraft((rows) =>
                        rows.filter((_, position) => position !== index)
                      )
                    }
                  >
                    <Trash2 />
                  </Button>
                </div>
              </div>
              <Input
                id={`marketplace-source-name-${row.rowId}`}
                value={row.name}
                onChange={(event) =>
                  updateRow(index, { name: event.target.value })
                }
              />
              <Label htmlFor={`marketplace-source-url-${row.rowId}`}>
                {t('Index URL')}
              </Label>
              <Input
                id={`marketplace-source-url-${row.rowId}`}
                type='url'
                inputMode='url'
                value={row.index_url}
                placeholder='https://example.com/index.json'
                onChange={(event) =>
                  updateRow(index, { index_url: event.target.value })
                }
              />
            </div>
          ))}
          <Button
            variant='outline'
            onClick={() =>
              setDraft((rows) => [
                ...rows,
                { name: '', index_url: '', rowId: makeRowId() },
              ])
            }
          >
            <Plus />
            {t('Add source')}
          </Button>
          <Alert>
            <AlertTitle>{t('Third-party source risk')}</AlertTitle>
            <AlertDescription>
              {t(
                'Anyone can publish an index. A plugin installed from a third-party source has the same access as one you upload by hand: review its source before installing.'
              )}
            </AlertDescription>
          </Alert>
        </div>
        <DialogFooter>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button
            disabled={invalidRow || saveMutation.isPending}
            onClick={() =>
              saveMutation.mutate(
                draft.map((row) => ({
                  name: row.name.trim(),
                  index_url: row.index_url.trim(),
                }))
              )
            }
          >
            {saveMutation.isPending ? t('Saving...') : t('Save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

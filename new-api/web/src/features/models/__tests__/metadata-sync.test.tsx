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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SyncWizardDialog } from '../components/dialogs/sync-wizard-dialog'
import type { MetadataSyncCandidate, MetadataSyncPreview } from '../types'

const preview: MetadataSyncPreview = {
  source: {
    locale: 'en',
    models_url: 'https://example.test/models.json',
    vendors_url: 'https://example.test/vendors.json',
    version: 'source-v1',
  },
  candidates: [
    {
      model_name: 'new-model',
      scope: 'site',
      kind: 'create',
      record_version: 'new-v1',
      vendor_to_create: 'Example vendor',
      fields: [{ field: 'description', local: '', upstream: 'New metadata' }],
    },
    {
      model_name: 'existing-model',
      scope: 'site',
      kind: 'update',
      record_version: 'existing-v1',
      fields: [
        { field: 'description', local: 'Local', upstream: 'Updated' },
        { field: 'status', local: 1, upstream: 0 },
      ],
    },
    {
      model_name: 'protected-model',
      scope: 'site',
      kind: 'blocked',
      record_version: 'protected-v1',
      fields: [],
    },
  ],
}

describe('metadata sync preview', () => {
  it('shows additions and field effects and writes only after explicit confirmation', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: preview },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          created_models: ['new-model'],
          updated_models: [
            { model_name: 'existing-model', fields: ['description'] },
          ],
          created_vendors: ['Example vendor'],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select new-model' })
    )
    await user.click(
      screen.getByRole('checkbox', { name: 'Select existing-model' })
    )
    expect(
      screen.queryByRole('checkbox', { name: 'Select protected-model' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('Metadata sync disabled')).toBeVisible()
    expect(post).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Preview selected changes' })
    )
    const description = screen.getByRole('checkbox', {
      name: 'Apply Description for existing-model',
    })
    const status = screen.getByRole('checkbox', {
      name: 'Apply Model square visibility for existing-model',
    })
    expect(description).not.toBeChecked()
    expect(status).not.toBeChecked()
    expect(
      screen.getByText(
        'Changes visibility in the model square. Channel status and existing API access are unchanged.'
      )
    ).toBeInTheDocument()
    await user.click(description)
    await user.click(
      screen.getByRole('button', { name: 'Review confirmation' })
    )
    expect(post).not.toHaveBeenCalled()
    expect(screen.getByText(/Example vendor/)).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Apply 2 model changes' })
    )
    await screen.findByText('Metadata sync completed')
    expect(post).toHaveBeenCalledWith('/api/models/sync_upstream', {
      locale: 'en',
      source_version: 'source-v1',
      selections: [
        {
          model_name: 'new-model',
          record_version: 'new-v1',
          create: true,
          fields: ['description'],
        },
        {
          model_name: 'existing-model',
          record_version: 'existing-v1',
          create: false,
          fields: ['description'],
        },
      ],
    })
    client.clear()
  })

  it('prioritizes syncable models and selects only eligible records across all pages', async () => {
    const syncable: MetadataSyncCandidate[] = Array.from(
      { length: 21 },
      (_, index) => ({
        model_name: `z-sync-${String(index + 1).padStart(2, '0')}`,
        scope: 'site',
        kind: 'create',
        record_version: `v${index}`,
        fields: [{ field: 'description', local: '', upstream: 'New metadata' }],
      })
    )
    const skipped: MetadataSyncCandidate[] = [
      'missing_upstream',
      'unchanged',
      'blocked',
      'missing_vendor',
    ].map((kind, index) => ({
      model_name: `a-skipped-${index}`,
      scope: 'site',
      kind: kind as MetadataSyncCandidate['kind'],
      record_version: `skip${index}`,
      fields: [],
    }))
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { ...preview, candidates: [...skipped, ...syncable] },
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          created_models: syncable.map((item) => item.model_name),
          updated_models: [],
          created_vendors: [],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    expect(
      await screen.findByText('25 models · 21 syncable · 4 skipped this time')
    ).toBeVisible()
    expect(
      within(screen.getByRole('table')).getAllByRole('row')[1]
    ).toHaveTextContent('z-sync-01')
    expect(screen.queryByText('a-skipped-0')).not.toBeInTheDocument()
    const pageSelection = screen.getByRole('checkbox', {
      name: 'Select syncable models on this page',
    })
    expect(pageSelection).not.toBeChecked()
    await user.click(pageSelection)
    expect(screen.getByText('20 selected models')).toBeVisible()
    await user.click(screen.getByRole('checkbox', { name: 'Select z-sync-01' }))
    expect(pageSelection).toBePartiallyChecked()
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(screen.getByText('z-sync-21')).toBeVisible()
    expect(screen.getByText('Not found upstream')).toBeVisible()
    for (const item of skipped) {
      expect(
        screen.queryByRole('checkbox', { name: `Select ${item.model_name}` })
      ).not.toBeInTheDocument()
    }
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Select syncable models on this page',
      })
    )
    expect(screen.getByText('20 selected models')).toBeVisible()
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Select syncable models on this page',
      })
    )
    expect(screen.getByText('19 selected models')).toBeVisible()
    await user.click(
      screen.getByRole('button', {
        name: 'Select all syncable models (all pages, 21)',
      })
    )
    expect(screen.getByText('21 selected models')).toBeVisible()
    expect(post).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Preview selected changes' })
    )
    expect(
      screen.queryByRole('button', { name: 'Select all changed fields' })
    ).not.toBeInTheDocument()
    expect(
      screen.getByText(
        'New models: all metadata shown below will be imported automatically. No selection is needed.'
      )
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Review confirmation' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Apply 21 model changes' })
    )
    await screen.findByText('Metadata sync completed')
    expect(post).toHaveBeenCalledWith('/api/models/sync_upstream', {
      locale: 'en',
      source_version: 'source-v1',
      selections: syncable.map((item) => ({
        model_name: item.model_name,
        record_version: item.record_version,
        create: true,
        fields: ['description'],
      })),
    })
    client.clear()
  })

  it('selects and clears changed fields across selected models while keeping new-model imports selected', async () => {
    const catalog: MetadataSyncCandidate = {
      ...preview.candidates[1],
      model_name: 'catalog-model',
      scope: 'catalog',
      record_version: 'catalog-v1',
      fields: [{ field: 'icon', local: 'Old icon', upstream: 'New icon' }],
    }
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { ...preview, candidates: [...preview.candidates, catalog] },
      },
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          created_models: ['new-model'],
          updated_models: [
            { model_name: 'existing-model', fields: ['description'] },
            { model_name: 'catalog-model', fields: ['icon'] },
          ],
          created_vendors: ['Example vendor'],
        },
      },
    })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    await screen.findByRole('checkbox', { name: 'Select existing-model' })
    await user.click(screen.getByRole('combobox', { name: 'Sync scope' }))
    await user.click(
      screen.getByRole('option', { name: 'Upstream model list' })
    )
    await user.click(
      screen.getByRole('button', {
        name: 'Select all syncable models (all pages, 3)',
      })
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Search models' }),
      'existing'
    )
    await user.click(
      screen.getByRole('button', { name: 'Preview selected changes' })
    )

    const selectAll = screen.getByRole('button', {
      name: 'Select all changed fields',
    })
    const clear = screen.getByRole('button', { name: 'Clear field selection' })
    const description = screen.getByRole('checkbox', {
      name: 'Apply Description for existing-model',
    })
    const status = screen.getByRole('checkbox', {
      name: 'Apply Model square visibility for existing-model',
    })
    const icon = screen.getByRole('checkbox', {
      name: 'Apply Icon for catalog-model',
    })
    const newDescription = screen.getByRole('checkbox', {
      name: 'Apply Description for new-model',
    })
    expect(screen.getByText('0 of 3 changed fields selected')).toBeVisible()
    expect(clear).toBeDisabled()
    expect(newDescription).toBeChecked()
    expect(newDescription).toHaveAttribute('aria-disabled', 'true')

    await user.click(description)
    expect(screen.getByText('1 of 3 changed fields selected')).toBeVisible()
    await user.tab({ shift: true })
    expect(clear).toHaveFocus()
    await user.tab({ shift: true })
    expect(selectAll).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(description).toBeChecked()
    expect(status).toBeChecked()
    expect(icon).toBeChecked()
    expect(selectAll).toBeDisabled()
    expect(screen.getByText('3 of 3 changed fields selected')).toBeVisible()

    await user.click(clear)
    expect(description).not.toBeChecked()
    expect(status).not.toBeChecked()
    expect(icon).not.toBeChecked()
    expect(newDescription).toBeChecked()
    expect(clear).toBeDisabled()
    expect(screen.getByText('3 selected models')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Review confirmation' })
    ).toBeEnabled()

    await user.click(selectAll)
    await user.click(status)
    expect(selectAll).toBeEnabled()
    expect(screen.getByText('2 of 3 changed fields selected')).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Review confirmation' })
    )
    expect(post).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole('button', { name: 'Apply 3 model changes' })
    )
    await screen.findByText('Metadata sync completed')
    expect(post).toHaveBeenCalledWith('/api/models/sync_upstream', {
      locale: 'en',
      source_version: 'source-v1',
      selections: [
        {
          model_name: 'new-model',
          record_version: 'new-v1',
          create: true,
          fields: ['description'],
        },
        {
          model_name: 'existing-model',
          record_version: 'existing-v1',
          create: false,
          fields: ['description'],
        },
        {
          model_name: 'catalog-model',
          record_version: 'catalog-v1',
          create: false,
          fields: ['icon'],
        },
      ],
    })
    client.clear()
  })

  it('disables confirmation after clearing all fields when only existing models are selected', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: preview },
    })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select existing-model' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Preview selected changes' })
    )
    expect(
      screen.getByText(
        'Existing models: select fields to replace their current values with the values on the right. Unselected fields stay unchanged.'
      )
    ).toBeVisible()
    const review = screen.getByRole('button', { name: 'Review confirmation' })
    expect(review).toBeDisabled()
    await user.click(
      screen.getByRole('button', { name: 'Select all changed fields' })
    )
    expect(review).toBeEnabled()
    await user.click(
      screen.getByRole('button', { name: 'Clear field selection' })
    )
    expect(review).toBeDisabled()
    expect(screen.getByText('1 selected models')).toBeVisible()
    client.clear()
  })

  it('preserves field choices and selections outside search and scope filters during bulk selection', async () => {
    const catalog: MetadataSyncCandidate = {
      ...preview.candidates[0],
      model_name: 'catalog-only',
      scope: 'catalog',
      vendor_to_create: undefined,
    }
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { ...preview, candidates: [...preview.candidates, catalog] },
      },
    })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select existing-model' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Preview selected changes' })
    )
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Apply Description for existing-model',
      })
    )
    await user.click(screen.getByRole('button', { name: 'Back' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Search models' }),
      'new-model'
    )
    expect(
      screen.getByText('1 selected outside the current filters')
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', {
        name: 'Select all syncable models (all pages, 1)',
      })
    )
    expect(screen.getByText('2 selected models')).toBeVisible()
    await user.clear(screen.getByRole('textbox', { name: 'Search models' }))
    await user.click(
      screen.getByRole('checkbox', { name: 'Only show syncable models' })
    )
    expect(screen.queryByText('protected-model')).not.toBeInTheDocument()
    await user.click(screen.getByRole('combobox', { name: 'Sync scope' }))
    await user.click(
      screen.getByRole('option', { name: 'Upstream model list' })
    )
    await user.click(
      screen.getByRole('button', {
        name: 'Select all syncable models (all pages, 3)',
      })
    )
    await user.click(screen.getByRole('combobox', { name: 'Sync scope' }))
    await user.click(
      screen.getByRole('option', { name: 'Models used on this site' })
    )
    expect(
      screen.getByText('1 selected outside the current filters')
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Preview selected changes' })
    )
    expect(
      screen.getByRole('checkbox', {
        name: 'Apply Description for existing-model',
      })
    ).toBeChecked()
    expect(
      screen.getByRole('checkbox', {
        name: 'Apply Model square visibility for existing-model',
      })
    ).not.toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Back' }))
    await user.click(screen.getByRole('button', { name: 'Clear selection' }))
    expect(screen.getByText('0 selected models')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Preview selected changes' })
    ).toBeDisabled()
    client.clear()
  })

  it('explains when no models are syncable and resets pagination when filtering to an empty result', async () => {
    const candidates: MetadataSyncCandidate[] = Array.from(
      { length: 21 },
      (_, index) => ({
        ...preview.candidates[2],
        model_name: `blocked-${index}`,
      })
    )
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { ...preview, candidates } },
    })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    expect(
      await screen.findByRole('button', {
        name: 'Select all syncable models (all pages, 0)',
      })
    ).toBeDisabled()
    expect(
      screen.getByRole('checkbox', {
        name: 'Select syncable models on this page',
      })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByText(
        'No models can be synced with the current filters. Adjust the search or sync scope.'
      )
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(screen.getByText('2 / 2')).toBeVisible()
    await user.click(
      screen.getByRole('checkbox', { name: 'Only show syncable models' })
    )
    expect(screen.getByText('No syncable models')).toBeVisible()
    expect(screen.getByText('1 / 1')).toBeVisible()
    await user.click(
      screen.getByRole('checkbox', { name: 'Only show syncable models' })
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Search models' }),
      'not-present'
    )
    expect(screen.getByText('No models match these filters')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Preview selected changes' })
    ).toBeDisabled()
    client.clear()
  })

  it('clears old selections when reloading fails and when the metadata language changes', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({ data: { success: true, data: preview } })
      .mockResolvedValueOnce({
        data: { success: false, message: 'Refresh failed' },
      })
      .mockResolvedValue({ data: { success: true, data: preview } })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select new-model' })
    )
    await user.click(
      screen.getByRole('button', { name: 'Load metadata preview' })
    )
    expect(await screen.findByText('Refresh failed')).toBeVisible()
    expect(screen.getByText('0 selected models')).toBeVisible()
    expect(
      screen.queryByRole('checkbox', { name: 'Select new-model' })
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Preview selected changes' })
    ).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select new-model' })
    )
    await user.click(
      screen.getByRole('combobox', { name: 'Metadata language' })
    )
    await user.click(screen.getByRole('option', { name: 'English' }))
    expect(screen.getByText('0 selected models')).toBeVisible()
    expect(
      screen.queryByRole('checkbox', { name: 'Select new-model' })
    ).not.toBeInTheDocument()
    client.clear()
  })

  it('keeps the write action unavailable when preview loading fails', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: false, message: 'Upstream unavailable' },
    })
    const post = vi.spyOn(api, 'post')
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <SyncWizardDialog open onOpenChange={() => {}} />
      </QueryClientProvider>
    )
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Load metadata preview' }))
    await waitFor(() =>
      expect(screen.getByText('Upstream unavailable')).toBeInTheDocument()
    )
    expect(
      screen.getByRole('button', { name: 'Preview selected changes' })
    ).toBeDisabled()
    expect(post).not.toHaveBeenCalled()
    client.clear()
  })
})

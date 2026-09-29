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
import { CHANNEL_TYPE_VLLM, CHANNEL_TYPE_SGLANG } from '../constants'
import { useChannels } from './channels-provider'
import { BalanceQueryDialog } from './dialogs/balance-query-dialog'
import { ChannelTestDialog } from './dialogs/channel-test-dialog'
import { CopyChannelDialog } from './dialogs/copy-channel-dialog'
import { EditTagDialog } from './dialogs/edit-tag-dialog'
import { FetchModelsDialog } from './dialogs/fetch-models-dialog'
import { InferenceStatusDialog } from './dialogs/inference-status-dialog'
import { MultiKeyManageDialog } from './dialogs/multi-key-manage-dialog'
import { OllamaModelsDialog } from './dialogs/ollama-models-dialog'
import { TagBatchEditDialog } from './dialogs/tag-batch-edit-dialog'
import { UpstreamUpdateDialog } from './dialogs/upstream-update-dialog'
import { ChannelMutateDrawer } from './drawers/channel-mutate-drawer'

export function ChannelsDialogs() {
  const { open, setOpen, currentRow, upstream } = useChannels()

  return (
    <>
      {open === 'inference-status' &&
        currentRow &&
        [CHANNEL_TYPE_VLLM, CHANNEL_TYPE_SGLANG].includes(currentRow.type) && (
          <InferenceStatusDialog
            key={currentRow.id}
            provider={
              currentRow.type === CHANNEL_TYPE_SGLANG ? 'sglang' : 'vllm'
            }
            channelId={currentRow.id}
            channelName={currentRow.name}
            onClose={() => setOpen(null)}
            onSyncModels={() => setOpen('fetch-models')}
            onTestChannel={() => setOpen('test-channel')}
          />
        )}
      {/* Channel Create/Update Drawer */}
      <ChannelMutateDrawer
        open={open === 'create-channel' || open === 'update-channel'}
        onOpenChange={(v) => !v && setOpen(null)}
        currentRow={open === 'update-channel' ? currentRow : null}
      />

      {/* Test Channel Dialog */}
      <ChannelTestDialog
        open={open === 'test-channel'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Balance Query Dialog */}
      <BalanceQueryDialog
        open={open === 'balance-query'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Fetch Models Dialog */}
      <FetchModelsDialog
        open={open === 'fetch-models'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Ollama Models Dialog */}
      <OllamaModelsDialog
        open={open === 'ollama-models'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Copy Channel Dialog */}
      <CopyChannelDialog
        open={open === 'copy-channel'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Multi-Key Management Dialog */}
      <MultiKeyManageDialog
        open={open === 'multi-key-manage'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Tag Batch Edit Dialog */}
      <TagBatchEditDialog
        open={open === 'tag-batch-edit'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Edit Tag Dialog */}
      <EditTagDialog
        open={open === 'edit-tag'}
        onOpenChange={(v) => !v && setOpen(null)}
      />

      {/* Upstream Model Update Dialog */}
      <UpstreamUpdateDialog upstream={upstream} />
    </>
  )
}

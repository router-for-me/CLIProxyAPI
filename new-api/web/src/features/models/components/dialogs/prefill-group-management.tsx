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
import { useEffect, useState } from 'react'

import type { PrefillGroup } from '../../types'
import { PrefillGroupFormDrawer } from '../drawers/prefill-group-form-drawer'
import { PrefillGroupManagementDialog } from './prefill-group-management-dialog'

type PrefillGroupManagementProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

type PrefillView = 'dialog' | 'drawer'

export function PrefillGroupManagement({
  open,
  onOpenChange,
}: PrefillGroupManagementProps) {
  const [view, setView] = useState<PrefillView>('dialog')
  const [currentGroup, setCurrentGroup] = useState<PrefillGroup | null>(null)

  useEffect(() => {
    if (!open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setView('dialog')

      setCurrentGroup(null)
    }
  }, [open])

  const handleDialogOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setView('dialog')
      setCurrentGroup(null)
      onOpenChange(false)
    }
  }

  const handleDrawerClose = () => {
    setView('dialog')
    setCurrentGroup(null)
  }

  const handleShowDrawer = (group: PrefillGroup | null) => {
    setCurrentGroup(group)
    setView('drawer')
  }

  return (
    <>
      <PrefillGroupManagementDialog
        open={open && view === 'dialog'}
        onOpenChange={handleDialogOpenChange}
        onCreateGroup={() => handleShowDrawer(null)}
        onEditGroup={(group) => handleShowDrawer(group)}
      />
      <PrefillGroupFormDrawer
        open={open && view === 'drawer'}
        onClose={handleDrawerClose}
        currentGroup={currentGroup}
      />
    </>
  )
}

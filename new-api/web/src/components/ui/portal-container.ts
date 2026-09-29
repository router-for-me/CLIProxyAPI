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
import * as React from 'react'

/**
 * Element that floating popups (Combobox, Select, ...) portal into instead of
 * `document.body`. Layered surfaces that disable pointer events or trap focus
 * outside themselves, such as the vaul-based Drawer, provide their own content
 * element so nested popups stay interactive. `undefined` keeps the default.
 */
const PortalContainerContext = React.createContext<
  React.RefObject<HTMLElement | null> | undefined
>(undefined)

function usePortalContainer() {
  return React.useContext(PortalContainerContext)
}

export { PortalContainerContext, usePortalContainer }

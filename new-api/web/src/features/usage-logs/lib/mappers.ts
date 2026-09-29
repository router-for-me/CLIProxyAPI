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
/**
 * Status mappers for different log types
 * Centralized mapper instances for consistent usage across components
 */
import {
  MJ_TASK_TYPE_MAPPINGS,
  MJ_STATUS_MAPPINGS,
  MJ_SUBMIT_RESULT_MAPPINGS,
  TASK_ACTION_MAPPINGS,
  TASK_STATUS_MAPPINGS,
  TASK_PLATFORM_MAPPINGS,
} from '../constants'
import { createStatusMapper } from './status'

// ============================================================================
// MjProxy (Drawing) Logs Mappers
// ============================================================================

/**
 * MjProxy task type mapper
 */
export const mjTaskTypeMapper = createStatusMapper(MJ_TASK_TYPE_MAPPINGS)

/**
 * MjProxy task status mapper
 */
export const mjStatusMapper = createStatusMapper(MJ_STATUS_MAPPINGS)

/**
 * MjProxy submit result mapper
 */
export const mjSubmitResultMapper = createStatusMapper(
  MJ_SUBMIT_RESULT_MAPPINGS
)

// ============================================================================
// Task Logs Mappers
// ============================================================================

/**
 * Task action type mapper
 */
export const taskActionMapper = createStatusMapper(TASK_ACTION_MAPPINGS)

/**
 * Task status mapper
 */
export const taskStatusMapper = createStatusMapper(TASK_STATUS_MAPPINGS)

/**
 * Task platform mapper
 */
export const taskPlatformMapper = createStatusMapper(TASK_PLATFORM_MAPPINGS)

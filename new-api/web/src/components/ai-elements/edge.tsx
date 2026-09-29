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
/* eslint-disable react-refresh/only-export-components */
import {
  BaseEdge,
  type EdgeProps,
  getBezierPath,
  getSimpleBezierPath,
  type InternalNode,
  type Node,
  Position,
  useInternalNode,
} from '@xyflow/react'

const Temporary = ({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
}: EdgeProps) => {
  const [edgePath] = getSimpleBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  return (
    <BaseEdge
      className='stroke-ring stroke-1'
      id={id}
      path={edgePath}
      style={{
        strokeDasharray: '5, 5',
      }}
    />
  )
}

const getHandleCoordsByPosition = (
  node: InternalNode<Node>,
  handlePosition: Position
) => {
  // Choose the handle type based on position - Left is for target, Right is for source
  const handleType = handlePosition === Position.Left ? 'target' : 'source'

  const handle = node.internals.handleBounds?.[handleType]?.find(
    (h) => h.position === handlePosition
  )

  if (!handle) {
    return [0, 0] as const
  }

  let offsetX = handle.width / 2
  let offsetY = handle.height / 2

  // this is a tiny detail to make the markerEnd of an edge visible.
  // The handle position that gets calculated has the origin top-left, so depending which side we are using, we add a little offset
  // when the handlePosition is Position.Right for example, we need to add an offset as big as the handle itself in order to get the correct position
  switch (handlePosition) {
    case Position.Left:
      offsetX = 0
      break
    case Position.Right:
      offsetX = handle.width
      break
    case Position.Top:
      offsetY = 0
      break
    case Position.Bottom:
      offsetY = handle.height
      break
    default:
      throw new Error(`Invalid handle position: ${handlePosition}`)
  }

  const x = node.internals.positionAbsolute.x + handle.x + offsetX
  const y = node.internals.positionAbsolute.y + handle.y + offsetY

  return [x, y] as const
}

const getEdgeParams = (
  source: InternalNode<Node>,
  target: InternalNode<Node>
) => {
  const sourcePos = Position.Right
  const [sx, sy] = getHandleCoordsByPosition(source, sourcePos)
  const targetPos = Position.Left
  const [tx, ty] = getHandleCoordsByPosition(target, targetPos)

  return {
    sx,
    sy,
    tx,
    ty,
    sourcePos,
    targetPos,
  }
}

const Animated = ({ id, source, target, markerEnd, style }: EdgeProps) => {
  const sourceNode = useInternalNode(source)
  const targetNode = useInternalNode(target)

  if (!(sourceNode && targetNode)) {
    return null
  }

  const { sx, sy, tx, ty, sourcePos, targetPos } = getEdgeParams(
    sourceNode,
    targetNode
  )

  const [edgePath] = getBezierPath({
    sourceX: sx,
    sourceY: sy,
    sourcePosition: sourcePos,
    targetX: tx,
    targetY: ty,
    targetPosition: targetPos,
  })

  return (
    <>
      <BaseEdge id={id} markerEnd={markerEnd} path={edgePath} style={style} />
      <circle fill='var(--primary)' r='4'>
        <animateMotion dur='2s' path={edgePath} repeatCount='indefinite' />
      </circle>
    </>
  )
}

export const Edge = {
  Temporary,
  Animated,
}

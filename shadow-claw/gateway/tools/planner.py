"""Planner tools: decompose complex tasks into steps.

Inspired by bytedance/deer-flow. The LLM itself acts as the planner —
the tool structures output and tracks plans in memory for multi-turn execution.
"""

import json
import time

from agent import tool

# In-memory plan storage (session-scoped)
_plans: dict[str, dict] = {}
_plan_counter = 0


@tool(
    "plan_task",
    "Decompose a complex objective into numbered steps. "
    "Use this when the user asks for help with a multi-step task. "
    "Returns a plan ID that can be referenced later.",
    {
        "type": "object",
        "properties": {
            "objective": {
                "type": "string",
                "description": "The complex task or objective to plan",
            },
            "steps": {
                "type": "array",
                "items": {"type": "string"},
                "description": "The numbered steps to achieve the objective",
            },
        },
        "required": ["objective", "steps"],
    },
)
async def plan_task(objective: str, steps: list[str]) -> str:
    global _plan_counter
    _plan_counter += 1
    plan_id = f"plan_{_plan_counter}"

    plan = {
        "id": plan_id,
        "objective": objective,
        "steps": [{"number": i + 1, "description": s, "status": "pending"} for i, s in enumerate(steps)],
        "created_at": time.time(),
    }
    _plans[plan_id] = plan

    lines = [f"Plan '{plan_id}': {objective}", ""]
    for step in plan["steps"]:
        lines.append(f"  {step['number']}. [{step['status']}] {step['description']}")

    return "\n".join(lines)


@tool(
    "plan_execute",
    "Mark a step in a plan as completed or in-progress. "
    "Use this to track progress on multi-step tasks.",
    {
        "type": "object",
        "properties": {
            "plan_id": {
                "type": "string",
                "description": "Plan ID (e.g., 'plan_1')",
            },
            "step_number": {
                "type": "integer",
                "description": "Step number to update",
            },
            "status": {
                "type": "string",
                "enum": ["in_progress", "completed", "skipped"],
                "description": "New status for the step",
            },
        },
        "required": ["plan_id", "step_number", "status"],
    },
)
async def plan_execute(plan_id: str, step_number: int, status: str = "completed") -> str:
    plan = _plans.get(plan_id)
    if plan is None:
        return f"Plan '{plan_id}' not found. Available plans: {', '.join(_plans.keys()) or 'none'}"

    for step in plan["steps"]:
        if step["number"] == step_number:
            step["status"] = status
            lines = [f"Plan '{plan_id}': {plan['objective']}", ""]
            for s in plan["steps"]:
                lines.append(f"  {s['number']}. [{s['status']}] {s['description']}")
            return "\n".join(lines)

    return f"Step {step_number} not found in plan '{plan_id}'."

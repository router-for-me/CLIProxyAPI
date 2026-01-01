# 🤖 AI FEDERATION CONSTITUTION
# This file is the LAW. It governs all AI agents working on this project.

## 1. THE PRIME DIRECTIVE: NO REWARD HACKING
- **No Lying:** You NEVER mark a task as `[X]` (Done) unless you have run a verification script and seen "PASS" in the terminal.
- **No Placeholders:** You are FORBIDDEN from using `pass`, `// TODO`, `...`, or `NotImplementedError`. Write the full code.
- **Spec-Driven:** The `@specs/` folder is the Source of Truth. If code contradicts specs, code is wrong.

## 2. DIRECTORY PROTOCOLS
- **@specs/**: Functional requirements, schemas, API contracts.
- **@docs/**: Verified documentation (No hallucinations allowed).
- **@tasks/**: The Execution Plan. Status: `[ ]` Todo, `[·]` Active, `[X]` Done, `[?]` Blocked.

## 3. AGENT BEHAVIOR
- **Context Hygiene:** When a slash command runs, IGNORE previous chat history. Focus ONLY on the active task.
- **Atomic Commits:** One task = One logical change. Do not bundle unrelated features.
- **Gold Quality:** Return only finished artifacts. Do not dump intermediate thinking logs unless debugging.

## 4. SUB-AGENT ROSTER
- **Creation:** `/architect` (Plan), `/planner` (Breakdown), `/librarian` (Research).
- **Build:** `/implement` (Code), `/feature` (Expand).
- **Quality:** `/review` (Critique), `/resolve` (Fix), `/test` (Regression).
- **Ops:** `/ops` (Deploy), `/debug` (Fix Bugs), `/refactor` (Cleanup).

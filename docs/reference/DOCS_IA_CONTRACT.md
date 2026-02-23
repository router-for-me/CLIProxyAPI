# Documentation IA Contract (cliproxyapi-plusplus)

## Purpose
Establish a strict information architecture contract so docs are readable, role-aware, and maintainable.

## Canonical Page Types (Divio)
1. `Tutorial`: step-by-step learning path for first successful outcome.
2. `How-to`: task-oriented recipe for known goal.
3. `Reference`: factual command/API/schema details.
4. `Explanation`: conceptual rationale, trade-offs, and design intent.

## Audience Lanes
1. `External User`: quickstart, install, first successful flow.
2. `Internal Developer`: architecture, module boundaries, contribution paths.
3. `Operator/SRE`: runbooks, health checks, incident paths.
4. `Contributor`: standards, style, change process, review expectations.

## Required Top-Level Surfaces
1. `Start Here`
2. `Tutorials`
3. `How-to Guides`
4. `Reference`
5. `Explanation`
6. `Operations`
7. `API`

## Page Contract
Every doc page must declare:
1. `Audience`
2. `Type`
3. `Prerequisites`
4. `Outcome`
5. `Last Reviewed`

## Quality Rules
1. No mixed-type pages (split into separate docs by type).
2. No orphan links (all nav links resolve).
3. No dump pages without summary and route context.
4. Every command snippet must be copy-safe and verified.
5. Every operator page must include verification commands.

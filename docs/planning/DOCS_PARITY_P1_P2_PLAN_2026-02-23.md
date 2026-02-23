# Docs Parity Plan P1-P2 (cliproxyapi-plusplus + thegent)

## Scope
Implement Phase 1 (Discovery baseline) and Phase 2 (IA contract + taxonomy) with parity across both repos.

## Phased WBS
1. `P1.1` Inventory active docs, nav routes, broken links, and audience gaps.
2. `P1.2` Produce parity rubric and score both sites.
3. `P1.3` Define canonical page types, audience lanes, and required surfaces.
4. `P2.1` Create IA contract docs in both repos.
5. `P2.2` Create migration matrix in both repos.
6. `P2.3` Align nav taxonomy targets (`Start Here`, `Tutorials`, `How-to`, `Reference`, `Explanation`, `Operations`, `API`).

## DAG Dependencies
1. `P1.2` depends on `P1.1`
2. `P1.3` depends on `P1.2`
3. `P2.1` depends on `P1.3`
4. `P2.2` depends on `P2.1`
5. `P2.3` depends on `P2.2`

## Acceptance Criteria
1. IA contract exists in both repos and names same page types and audience lanes.
2. Migration matrix exists in both repos with identical mapping rules.
3. Planning document captures DAG and parity acceptance criteria.
4. No docs placed outside approved `docs/` structure.

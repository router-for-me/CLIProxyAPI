# CLIProxyAPIPlus Issue Wave: Remaining Open Issues (Next Batch)

Requested: "next 70 issues"  
Current GitHub open issues available: 52 total.  
Already dispatched in previous batch: 20.  
Remaining in this batch: 32.

Source query:
- `gh issue list --state open --limit 200 --json number,title,updatedAt,url`
- Date: 2026-02-22

Execution lanes (6-way parallel on `workstream-cpbv2` worktrees):

## Lane 2 -> `../cliproxyapi-plusplus-wave-cpb-2`
- #169
- #165
- #163
- #158
- #160
- #149

## Lane 3 -> `../cliproxyapi-plusplus-wave-cpb-3`
- #147
- #146
- #145
- #136
- #133
- #129

## Lane 4 -> `../cliproxyapi-plusplus-wave-cpb-4`
- #125
- #115
- #111
- #102
- #101

## Lane 5 -> `../cliproxyapi-plusplus-wave-cpb-5`
- #97
- #99
- #94
- #87
- #86

## Lane 6 -> `../cliproxyapi-plusplus-wave-cpb-6`
- #83
- #81
- #79
- #78
- #72

## Lane 7 -> `../cliproxyapi-plusplus-wave-cpb-7`
- #69
- #43
- #37
- #30
- #26

Dispatch contract per lane:
- Investigate all assigned issues.
- Implement feasible, low-risk fixes.
- Add/update tests for behavior changes.
- Run targeted tests for touched packages.
- Write lane report in `docs/planning/reports/issue-wave-gh-next32-lane-<n>.md`.

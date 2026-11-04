# 2026-11-04: Opus agent, Haiku delegate

Claude Code with `--model opus`, delegate `DETOUR_MODEL=haiku` (through the same Claude login), default policy: 350-line threshold, 350-line per-file session budget. 3 interleaved reps per scenario and variant. Detour commit `7e1ce90` plus local-repo support. Every session answered correctly (60/60).

**Claude $** is Claude Code's `total_cost_usd` (list price) for the agent plus the Haiku delegations, since both draw on the same plan.

## cobra v1.10.2 (36 sessions)

| Scenario | Detour engaged? | Claude $ baseline → detour | Change | Time s |
|---|---|---|---|---|
| completions-design | yes (delegated, 0.7 blocks/session) | 0.241 ± 0.028 → 0.183 ± 0.031 | **−24.0%** | 33 → 63 |
| execute-flow | yes (2.3 blocks/session) | 0.275 ± 0.013 → 0.262 ± 0.002 | **−4.9%** | 49 → 82 |
| run-hooks | no | 0.037 → 0.033 | −11.0% (noise) | 9 → 8 |
| completion-directives | no | 0.048 → 0.066 | +38.8% (noise) | 7 → 8 |
| unknown-command | no | 0.049 → 0.038 | −22.9% (noise) | 8 → 6 |
| small-file-control | no | 0.025 → 0.023 | −8.3% (noise) | 4 → 5 |

## Private PHP/TypeScript monorepo, ~10k PHP files (24 sessions)

Scenarios about 1,000–1,500-line domain aggregates. Names withheld; the repository is private.

| Scenario | Detour engaged? | Claude $ baseline → detour | Change | Time s |
|---|---|---|---|---|
| cross-file flow (3 files of 700–1,500 lines) | yes (2 blocks/session) | 0.571 ± 0.035 → 0.475 ± 0.058 | **−16.7%** | 57 → 157 |
| state machines of one 1,500-line aggregate | yes (3 blocks/session) | 0.339 ± 0.046 → 0.324 ± 0.040 | −4.4% (within noise) | 73 → 74 |
| rules of one 1,350-line aggregate | no (0 blocks, 0 delegations) | 0.217 ± 0.078 → 0.156 ± 0.023 | −28.0% (noise) | 22 → 27 |
| small-file control | no | 0.047 → 0.040 | −14.5% (noise) | 7 → 8 |

## What this says

- **Session-to-session noise is ±15–30%** with 3 reps. Controls and scenarios where detour never engaged moved by as much. Only differences well beyond the sd mean anything.
- **Lookup questions don't trigger detour.** Opus answers them with Grep plus a small targeted read. Nothing to save.
- **Comprehension of large files is where it pays:** −24% (module design overview) and −17% (flow across three large files), both beyond the noise. Single-file questions answered by a few targeted ranges saved ~5%.
- **It costs time:** sessions ran 1.6–2.8× longer, mostly waiting for Haiku.
- **Far from Spotify's 90%.** That figure counts the read tokens Claude avoided. This measures the whole session, delegate included, with a strong agent that already reads selectively.
- **Without the per-file budget, detour did nothing.** Blocked whole reads turned into chunked reads (pilot, not in these tables).

## Seen in the transcripts, worth fixing

- After the first block Opus tried one or two smaller chunks (also blocked by the budget) before running `detour read`. Each try is a wasted turn. The first denial should state the remaining budget so the agent goes straight to delegation.
- Opus often asked `detour read` about a line range it had found with Grep ("for lines 736–1180..."). Sending only that range would cut delegate tokens and latency.

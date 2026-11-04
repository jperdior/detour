# 2026-11-04 (v2): budget notice and line ranges

Same setup as [the first run](2026-11-04-opus-haiku.md): Opus agent, Haiku delegate, 350-line threshold and per-file budget. Changes under test (commit `625c6cb`):

- the first whole-file denial states how many chunked lines are left for that file;
- `detour read` accepts `FILE:START-END`.

Only the four scenarios where detour engaged in v1, now with 5 interleaved reps (40 sessions). All answers were correct (40/40).

| Scenario | v1 change | **v2 Claude $ baseline → detour** | **v2 change** | Blocks/session v1 → v2 | Time s (v2) |
|---|---|---|---|---|---|
| cobra completions-design | −24.0% | 0.226 ± 0.014 → 0.132 ± 0.020 | **−41.5%** | 0.7 → 0.2 | 32 → 56 |
| cobra execute-flow | −4.9% | 0.257 ± 0.022 → 0.238 ± 0.023 | −7.1% | 2.3 → 1.0 | 48 → 80 |
| private: cross-file flow | −16.7% | 0.588 ± 0.044 → 0.375 ± 0.068 | **−36.2%** | 2.0 → 1.4 | 61 → 114 |
| private: one aggregate's state machines | −4.4% | 0.319 ± 0.017 → 0.306 ± 0.026 | −4.1% (noise) | 3.0 → 1.0 | 43 → 67 |

Delegations: 26, of which 6 used a line range (median 12k delegate input tokens and 18 s, against 23k and 22 s for whole files).

## Reading

- **The notice worked:** blocks per session fell to about one. The agent delegates after the first denial instead of trying smaller chunks.
- **Multi-file and module-level comprehension now saves 36–42%**, well beyond the sd. Questions about one file that a few targeted ranges can answer still save ~5%: there is little to cut.
- **Time is still 1.5–1.9× the baseline.** The delegate's latency (about 20 s per call, mostly `claude -p` start-up and Haiku generation) is now the main cost of using detour.
- v1 and v2 baselines differ by a few percent (cache state, server load). Compare variants within a run, not across runs.

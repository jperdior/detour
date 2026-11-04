# 2026-11-04 (v4): block-only vs full detour

Does the saving come from blocking whole reads, or from delegating them? Three variants, interleaved, 3 reps each, Opus agent:

- **baseline**: plain Claude Code.
- **block**: a hook that denies whole reads of files over 350 lines and points the agent to Grep and targeted reads. No delegation, no skill, no file budget (`detour hook claude --block-only`, plugin in `plugin-block/`).
- **detour**: the full plugin as in [v2](2026-11-04-v2-budget-notice-ranges.md) and [v3](2026-11-04-v3-editing.md), with Haiku as delegate.

The tasks are the six where detour engaged before: four comprehension questions (two on cobra, two in a private PHP monorepo) and the two private editing tasks scored by hidden tests. 54 sessions, all passed.

| Task | baseline Claude $ | block | detour |
|---|---|---|---|
| cobra completions-design (comprehension) | 0.243 | **+8.0%** | **−37.0%** |
| cobra execute-flow (comprehension) | 0.290 | −1.5% | +2.0% |
| private cross-file flow (comprehension) | 0.615 | +0.4% | **−33.4%** |
| private state machines of one aggregate (comprehension) | 0.331 | −0.9% | +2.0% |
| private: method + exception, 1,350-line aggregate (edit) | 0.546 | −8.9% | −4.0% |
| private: method, 1,500-line aggregate (edit) | 0.495 | −18.7% | −12.0% |

## Reading

- **For comprehension, blocking alone saves nothing.** Denied a whole read, the agent pages through the file in chunks: Read calls went from 3.7 to 14.7 per session on the cross-file flow. Cost is the same as the baseline. With delegation the two module-level questions save 33–37% again, as in v2.
- **For editing, block and detour behave alike**: no delegations either way, the agent switches to Grep plus targeted reads, and both save roughly 5–20%. With 3 reps and an sd of 0.04–0.08, the difference between block and detour on these tasks is noise.
- **So the delegation is what makes detour work** where the large savings are. A block-only hook would capture the smaller editing saving and lose the comprehension one. The per-file budget, which block-only lacks, is what stops the paging.

The field trial ([internal/trial](../../internal/trial)) therefore compares off vs full detour.

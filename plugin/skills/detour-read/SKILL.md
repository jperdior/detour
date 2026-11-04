---
name: detour-read
description: Use when you need to understand a large file (over ~350 lines) or answer a question that spans several files. Delegates the reading to a cheap model with `detour read` and returns a compact answer with line numbers. Also use when a Read or cat is denied with a "detour:" message.
---

# Reading large files with detour

Large files are expensive to read into your context. Ask a cheap model about them instead.

## How

```bash
detour read -q "<specific question>" path/to/file.go [more files...]
```

- Put every file the question needs in one call. One call with three files is cheaper than three calls.
- If you already know which lines matter (from Grep, or an earlier answer), send only that range: `path/to/file.go:736-1180`. It is faster and cheaper than the whole file.
- Targeted reads of a large file are capped per session (the denial message says how many lines are left). Once you are blocked, don't try smaller chunks: delegate.
- Ask a specific question: name the functions, types or behaviour you need. "Explain this file" returns a long, less useful answer.
- The answer cites lines as `file:L123`. To see exact code, Read only that range: `Read(file, offset=120, limit=40)`.

## When not to use it

- You are about to edit the file: read the exact section you will change with offset and limit.
- Debugging subtle behaviour (concurrency, security, off-by-one): the cheap model misses details. Read the relevant ranges yourself.
- Small files (under ~350 lines): read them directly; delegation is slower.

If `detour read` fails, fall back to Grep and targeted Reads. Do not retry the same whole-file read.

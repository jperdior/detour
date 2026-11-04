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
- Ask a specific question: name the functions, types or behaviour you need. "Explain this file" returns a long, less useful answer.
- The answer cites lines as `file:L123`. To see exact code, Read only that range: `Read(file, offset=120, limit=40)`.

## When not to use it

- You are about to edit the file: read the exact section you will change with offset and limit.
- Debugging subtle behaviour (concurrency, security, off-by-one): the cheap model misses details. Read the relevant ranges yourself.
- Small files (under ~350 lines): read them directly; delegation is slower.

If `detour read` fails, fall back to Grep and targeted Reads. Do not retry the same whole-file read.

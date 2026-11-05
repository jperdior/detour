# detour field trial

detour is meant to make Claude Code cheaper by having a small model (Haiku) read large files instead of your main model. The benchmarks say it saves 33–42% on some tasks and nothing on others. This trial measures whether it saves anything in **real, everyday work**.

## How it works

- **The draw.** Every new Claude Code session you start in the repositories you pick (their worktrees included) is assigned at random, 50/50:
  - **off**: nothing changes. detour only notes which reads it would have blocked.
  - **detour**: whole reads of large code files (over 350 lines) are blocked, and Claude asks Haiku about them instead.
- **Loops and scripts too.** Headless sessions (`claude -p`, agent loops, CI) are drawn as well. There detour never denies a call: it cuts the read to one line and tells Claude to use `detour read`, so the tool driving Claude sees no permission denial.
- **The measurement.** When the session ends, detour measures its cost at list price from the transcript Claude Code already keeps on your machine.
- **The end.** After 100 sessions the trial ends and detour switches off. At the start of your next session Claude tells you and asks whether to send the anonymized results; it shows you the report and publishes it only with your permission. Then it offers to remove detour. If you say "not now", it asks again in your next session.

Work as usual; there is nothing else to do.

## Privacy and cost

- **Nothing leaves your machine until you run `detour trial share`**, and that command shows you the report first.
  - The report holds numbers per session: cost, tokens by type, number of calls, edits, blocks and delegations.
  - It holds no code, file paths, prompts, repository names or session ids. Projects appear as `project-1`, `project-2`…
  - It is sent as a public GitHub issue in `jperdior/detour`, under your GitHub account.
- **Haiku only, through your own Claude Code login**, so delegations count against your own plan like any other usage. Trial sessions never send code to a third-party provider, even if detour is configured for one.
- **Only source code is affected.** Markdown and text files (specs, plans, ADRs, docs) and files outside the repository are never blocked: Claude reads them whole, as usual.
- In detour sessions you will sometimes see a `detour:` denial and a `detour read` command. Expect a few extra seconds when Claude delegates.

## Requirements

- macOS or Linux.
- Claude Code, logged in (subscription or API key).
- Git repositories you work in regularly.
- Optional: `gh`, logged in, to send the report with one command.

## Set up by hand (2 minutes)

```bash
curl -fsSL https://raw.githubusercontent.com/jperdior/detour/main/install.sh | sh
detour trial setup ~/code/repo-one ~/code/repo-two
detour trial status
```

If you run Claude Code with more than one profile (for example an alias that sets `CLAUDE_CONFIG_DIR=~/.claude-personal`), add each one:

```bash
detour trial setup --config-dir ~/.claude --config-dir ~/.claude-personal ~/code/repo-one
```

**Do not also install the detour plugin** (`claude plugin install detour`). The plugin turns detour on in every session, which would spoil the comparison. If you have it, remove it with `claude plugin uninstall detour@detour`.

Sessions that were already open keep running without the trial. It starts with the next session you open.

## Or let Claude set it up

In Claude Code, paste:

> Set me up for the detour field trial following https://raw.githubusercontent.com/jperdior/detour/main/docs/TRIAL.md (section "Instructions for the agent").

## When Claude tells you the trial is done

```bash
detour trial report            # your own results
detour trial share --dry-run   # exactly what would be sent
detour trial share             # send it (asks before publishing)
```

Then remove it: `detour trial uninstall` cleans every Claude Code profile setup installed into.

## Pause or remove

- **Pause:** set `enabled: false` in `~/.config/detour/trial.yaml`.
- **Remove:** `detour trial uninstall`. It removes the hooks from every profile setup installed into and keeps your measurements, so you can still share them.

---

## Instructions for the agent

You are helping the user join the detour field trial. Follow these steps in order and keep the user informed. Ask before every step that changes their machine. Do not do anything beyond these steps.

1. **Check the basics.**
   - Run `claude --version` and `git --version`.
   - Run `uname -s` and confirm the OS is macOS or Linux; otherwise stop and say the trial is not supported there.
   - Run `detour version`. If detour is missing, ask whether to install it, then run `curl -fsSL https://raw.githubusercontent.com/jperdior/detour/main/install.sh | sh`. If the script says the install directory is not on PATH, tell the user the line it printed to add to their shell profile.
2. **Choose the repositories.**
   - Ask which git repositories they work in most. Suggest candidates from the current directory and recent Claude Code projects: the directory names under `~/.claude/projects` encode their paths.
   - Two to four repositories is a good number. Prefer ones with large source files.
3. **Find the Claude Code profiles.**
   - Run `grep -n CLAUDE_CONFIG_DIR ~/.zshrc ~/.bashrc ~/.zprofile ~/.bash_profile 2>/dev/null` and `ls -d ~/.claude*`.
   - If they use more than one config dir, ask which ones they use for these repositories. Each needs its own `--config-dir`.
4. **Check for the plugin.** Run `claude plugin list 2>/dev/null | grep -i detour`. If the detour plugin is installed, explain why it must be removed (it turns detour on in every session) and offer `claude plugin uninstall detour@detour`.
5. **Run the setup.**
   - Run `detour trial setup [--config-dir DIR]... REPO...`.
   - It edits the user's Claude Code `settings.json`, keeping a backup at `settings.json.before-detour`. If the permission system refuses to let you run it, ask the user to run it themselves by typing `! ` followed by the exact command.
6. **Verify.** Run `detour trial status`. It should print `enabled true` and the projects. Tell the user:
   - The trial starts with their next new session in those repositories; this session is not part of it.
   - They work as usual.
   - After 100 sessions, Claude will tell them at the start of a session and ask permission to send the report.
7. **When they later ask you to share the results:**
   - Run `detour trial share --dry-run` and show them the report.
   - Ask for explicit confirmation that it may be published as a public GitHub issue.
   - Only then run `detour trial share --yes`.

# otop

`htop`, but for [opencode](https://github.com/sst/opencode). curses TUI that shows all your running sessions at a glance — status, tokens, uptime, what the model last said, etc.

built because i run like 5-10 opencode sessions simultaneously and needed a way to see what's happening without switching through all of them :>

## install

```
pipx install otop
```

or from source:
```
git clone https://github.com/fadedlamp42/otop
cd otop && pipx install .
```

no dependencies beyond python stdlib.

## usage

just run `otop` in your terminal.

each session gets two rows — title + metrics on top, last model output + secondary info on bottom. status is color-coded: green = active (generating, tool use, busy), yellow = transitional (thinking, queued), white = idle.

press `enter` on any session to open a detail view with the session's message history.

### keys

```
q         quit
enter     detail view for selected session
r         force refresh
j/k       scroll (arrow keys too)
>/<       cycle sort column
s         flip sort direction
/         filter (matches title, model, tty, status, etc.)
y         yank session ID to clipboard
a         toggle non-interactive sessions (commit-msg, subagents)
p         toggle background processes (LSPs, tool wrappers)
t         todo panel for selected session
m         MCP server config panel
```

detail view: `esc` to go back, `j/k` to scroll.

## how it works

the hard part is figuring out which process is running which session — opencode doesn't write a PID file or expose this anywhere. we solve it with a three-tier correlation:

1. **explicit `-s` flag** in the cmdline (if you ran `opencode -s ses_xxx`)
2. **log filename timestamps** — opencode writes to `~/.local/share/opencode/log/<UTC-timestamp>.log`. even after rotation deletes the file, `lsof` still sees the fd. we extract the start time and match it against message activity in the db
3. **fallback** — most recently updated session for that working directory

when multiple processes share the same cwd, a two-pass claimed-set algorithm ensures each process gets a unique session match. older processes get first pick since they have more message history to correlate against.

status is inferred from the db's `finish` field on assistant messages, cross-referenced with CPU usage from `ps` as a secondary signal (catches mid-stream responses that haven't been flushed to the db yet).

## limitations

- **macOS only** right now — `lsof` parsing and cwd resolution are macOS-flavored. linux would need `/proc/<pid>/cwd` and `/proc/<pid>/fd/` instead
- reads from `~/.local/share/opencode/opencode.db` read-only (WAL mode, safe to query while opencode is writing)
- no cost tracking — opencode reports 0 for most providers in the db anyway

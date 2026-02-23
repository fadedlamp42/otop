"""
opencode-htop — curses TUI monitor for all running opencode sessions.

built in session ses_3846f1ddeffelJOeVWgHV5TVGR from ~
(peter + claude-opus-4-6, feb 2026)

# data sources

reads from three places, all read-only:

  1. ~/.local/share/opencode/opencode.db
     sqlite database (WAL mode). contains all session, message, part, and todo
     data. opened with ?mode=ro to avoid interfering with running instances.
     schema: session, message (json data blob), part, todo, project, permission.

  2. ps + lsof (system process table)
     `ps axo pid,pcpu,rss,tty,etime,args` finds running opencode processes.
     a single `lsof -p <all-pids>` call extracts each process's cwd and the
     path of its open log file (used for start-time inference).

  3. ~/.config/opencode/opencode.json
     global config file containing MCP server definitions (name, type, enabled).
     per-project configs could also exist as opencode.json in project roots,
     but we only read the global one for now.


# PID-to-session correlation

the hardest problem: opencode doesn't write a pid file or expose which session
a process is running. we solve this with a three-tier strategy:

  tier 1: explicit `-s <session_id>` in the cmdline (gold standard)
    parsed from `ps` output with a regex. users who run `opencode -s ses_xxx`
    or `opencode --session ses_xxx` give us a direct mapping.

  tier 2: log-filename start time + message activity correlation
    each opencode process writes to its own log file at
    ~/.local/share/opencode/log/<UTC-timestamp>.log. opencode aggressively
    rotates (unlinks) these files, so they're deleted from disk, but unix
    keeps the fd alive as long as the process holds it open. `lsof` still
    reports the original path even after unlink, so we can extract the filename.

    the filename encodes the process start time in UTC (e.g. 2026-02-20T145658.log).
    IMPORTANT: these are UTC, not local time. an early iteration parsed them as
    local time and got 8-hour offsets that broke all the correlations.

    with the start time, we query the db for sessions matching the process's cwd
    that have messages created *after* the process started. ranked by message
    count descending — the session with the most activity since this process
    started is almost certainly the one it's running.

  tier 3: fallback — most recently updated session for the process's cwd
    if no log file is found or no messages match, we just pick the session in
    the matching directory with the newest time_updated. this is the weakest
    signal but still usually correct for single-session-per-directory cases.


# disambiguation: the claimed-set two-pass algorithm

when multiple processes share the same cwd (e.g. two terminals both in ~/),
they'd naively both resolve to the same session. we solve this with two passes:

  pass 1: processes with explicit -s flags claim their sessions immediately.
  pass 2: remaining processes are sorted by start_time ascending (earliest first).
          each process claims the best unclaimed session from its candidates.
          once a session is claimed, it's removed from the pool.

this means the older process (which has been accumulating messages longer) gets
the higher-activity session, and the newer one falls through to the next best
match. tested with 5 simultaneous processes including two sharing cwd=~/,
correctly resolved all five to unique sessions.


# status inference

opencode's message.data json has a `finish` field on assistant messages:
  - null or ""  → response still streaming (not yet committed to db)
  - "tool-calls" → model wants to call tools, opencode is executing them
  - "stop"       → model finished its turn, waiting for user
  - "length"     → hit output token limit, truncated

but there's a gap: when the model is mid-response, the db often hasn't flushed
yet, so the last message looks stale. we cross-reference CPU usage (from ps) as
a secondary signal: if a process is using >5% CPU but its db state looks idle,
it's probably actively working on something. this catches:
  - long tool executions (bash commands, file reads)
  - model responses that haven't been committed to db yet
  - MCP server interactions


# token accounting

opencode splits token usage across four fields per assistant message:
  - tokens.input:       uncached input tokens (new content the model sees)
  - tokens.cache.read:  tokens served from prompt cache (still part of context)
  - tokens.cache.write: tokens written to cache for future reads
  - tokens.output:      tokens the model generated

the "input" field alone is misleadingly small (often single digits) because most
context comes from cache reads. for display, we show:
  context = input + cache.read  (the actual prompt size the model processes)
  output  = output              (what the model generates)

cost is currently reported as 0 for most providers in the db (opencode may not
track cost for all providers). shown as "-" when zero.


# keys — list view

  q / ctrl-c  quit
  enter       open detail view for the selected session
  r           force refresh
  t           toggle todos panel for selected session
  m           toggle global MCP server config panel
  y           yank (pbcopy) the selected session's ID
  j/k         scroll session list (also arrow keys)
  > / .       cycle sort column forward (like htop)
  < / ,       cycle sort column backward
  s           flip sort direction (asc/desc)
  /           enter filter mode (k9s-style, matches title/model/cwd/tty/status)
  enter       commit filter (when in filter input mode)
  esc         clear filter (in filter mode: clear + exit; outside: just clear)

# keys — detail view

  esc / q     back to session list
  r           refresh capture / messages
  tab         toggle between tmux live view and db message history
  j/k         scroll content (also arrow keys)
  d / pgdn    scroll half page down
  u / pgup    scroll half page up


# detail view

  pressing enter on a session row opens a full-screen detail view with two
  data sources, toggled with tab:

    tmux (primary): if the process's TTY maps to a tmux pane, we run
      `tmux capture-pane -t <target> -p` to grab the live terminal screen.
      this shows the actual opencode TUI including tool outputs, progress,
      and chrome. auto-refreshes every 2 seconds for a live view.

    db (fallback): if tmux isn't available, shows the last 30 messages from
      the database with timestamps, role, finish status, token usage, and
      a preview of the text content. useful for seeing conversation history.


# process title

  on startup, emits tmux escape sequences to set the window name to
  "opencode-htop" (fixes tmux automatic-rename showing "python3").
  also sets the xterm title and macOS pthread name for Activity Monitor.


# two-row columnar layout

  each session takes two screen rows. columns are vertically paired so
  related values stack directly above/below each other. the header row
  labels both the top and bottom values for each column cell.

  the layout evolved through three iterations:
    v1: single row per session, all columns in one line. worked but was
        too dense to scan, and the title got squeezed by fixed columns.
    v2: two rows, but title/last on row 1 and ALL metrics on row 2.
        the user clarified: the A/B pairs should be top/bottom within
        the same column cell, not split into separate row purposes.
    v3 (current): columnar grid where each column has a top and bottom
        value, both labeled in a two-line header. this aligns related
        data vertically and makes the grid scannable.

  column cells (top / bottom):

    TITLE / LAST    session title / last line of most recent assistant text.
                    flexible width — gets all remaining space after fixed cols.
    STATUS / MSGS   inferred status / total message count in the session.
                    status: open, working, streaming, responding, etc.
    SID / PID       full session ID / process ID. the full SID is shown
                    (30 chars) because truncated SIDs can't be used with
                    `opencode -s`. this was an explicit design decision after
                    testing showed 8-char truncations don't resolve.
    UP / ROUND      uptime (process elapsed time) / round duration (time since
                    the last user message — how long the current round has been).
    CPU / MEM       CPU% from ps / resident memory in MB. CPU% is also used as
                    a secondary signal for status inference (>5% = active).
    CTX / OUT       context tokens (input + cache.read) / output tokens.
                    split into two rows because the combined "14.8M/84.8K"
                    format was ambiguous about which was which.
    MODEL / TTY     abbreviated model name / terminal device. TTY is needed
                    for the tmux pane mapping in the detail view.

  cost was removed: too abstract to calculate accurately with provider
  discounts and prompt caching. the db often reports 0 anyway.


# known limitations

  - log file rotation: if opencode hasn't written to the log recently and
    the file gets fully garbage-collected, we lose the start-time signal
    and fall back to tier 3 matching.
  - per-project MCP config: we only read the global opencode.json, not
    per-project configs. a future iteration could resolve per-session MCPs
    by reading opencode.json from the session's directory.
  - no listening ports: opencode instances don't expose HTTP unless started
    with `opencode serve`. we can't query them live for richer state.
  - macOS only: lsof parsing and cwd resolution are macOS-flavored. linux
    would need /proc/<pid>/cwd and /proc/<pid>/fd/ instead.
  - tmux detail view: only works when opencode-htop itself runs inside tmux
    (which it almost certainly does, given the use case).
"""

import calendar
import curses
import json
import os
import re
import signal
import sqlite3
import subprocess
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional


# -- paths --

DB_PATH = Path.home() / ".local" / "share" / "opencode" / "opencode.db"
CONFIG_PATH = Path.home() / ".config" / "opencode" / "opencode.json"
REFRESH_INTERVAL = 2.0


# -- data types --
#
# these dataclasses represent the two halves of the data model:
#   ProcessInfo — what we know from the OS (ps, lsof). no db dependency.
#   SessionInfo — what we know from opencode's sqlite db. no process dependency.
# the TUI's job is to correlate these two via the PID-to-session algorithm.


@dataclass
class ProcessInfo:
    """an opencode process found via `ps`. all fields come from the OS."""

    pid: int
    cpu_percent: float  # from ps pcpu — used for status inference (>5% = active)
    mem_mb: float  # from ps rss (converted from KB)
    elapsed: str  # raw etime string from ps (not used for display, kept for debugging)
    tty: str  # terminal device, e.g. "ttys005". used to map to tmux panes.
    cwd: str  # from lsof — the process's current working directory
    cmdline: str  # full command line from ps args column
    session_id: Optional[str] = None  # tier 1: parsed from -s flag in cmdline
    start_time_ms: int = 0  # tier 2: UTC epoch ms, derived from log filename via lsof
    is_tool_process: bool = False  # True for `opencode run` (LSPs, one-shot wrappers)


@dataclass
class SessionInfo:
    """a session from opencode's sqlite db. all fields come from SQL queries."""

    session_id: str
    title: str
    directory: str
    project_id: str
    model: str  # from the most recent message's modelID field
    agent: str  # from the most recent message's agent field
    message_count: int
    total_input_tokens: (
        int  # context = uncached input + cache.read (see token accounting)
    )
    total_output_tokens: int
    total_cache_read: int  # kept separate for potential future display
    total_cost: float  # usually 0 — opencode doesn't reliably track cost
    last_finish: Optional[str]  # "stop", "tool-calls", "length", null, or ""
    last_message_role: str  # "user" or "assistant" — drives status inference
    last_message_time: int  # epoch ms of most recent message
    time_created: int  # epoch ms when session was first created
    time_updated: int
    round_start_time: int = (
        0  # epoch ms of most recent user message (current round start)
    )
    last_output: str = (
        ""  # last non-empty line from the most recent assistant text part
    )
    active_todos: list = field(default_factory=list)  # TodoItem list for the 't' panel
    version: str = ""
    interactive: bool = (
        True  # False when permission IS NOT NULL (commit-msg, subagents, etc.)
    )


@dataclass
class TodoItem:
    content: str
    status: str  # pending, in_progress, completed, cancelled
    priority: str  # high, medium, low


# -- data collection --


def _parse_log_timestamp(logpath):
    """parse a log filename into epoch ms.

    log filenames are UTC timestamps like 2026-02-20T145658.log.
    IMPORTANT: must be parsed as UTC, not local time. an early version of this
    script parsed as local and produced 8-hour offsets that broke correlation.
    """
    basename = os.path.basename(logpath)
    m = re.match(r"(\d{4})-(\d{2})-(\d{2})T(\d{2})(\d{2})(\d{2})\.log", basename)
    if m:
        dt = datetime(
            int(m.group(1)),
            int(m.group(2)),
            int(m.group(3)),
            int(m.group(4)),
            int(m.group(5)),
            int(m.group(6)),
            tzinfo=timezone.utc,
        )
        return int(dt.timestamp() * 1000)
    return 0


def _batch_lsof(pids):
    """single lsof call for all PIDs; returns {pid: {cwd, logpath}}.

    batching into one call avoids N separate lsof invocations (which are ~200ms
    each on macOS). the log path is extracted even if the file has been unlinked
    from disk — unix keeps the inode alive while the fd is open, and lsof still
    reports the original path. this is the mechanism that makes tier 2 start-time
    correlation possible.
    """
    if not pids:
        return {}
    pid_args = ",".join(str(p) for p in pids)
    result_map = {p: {"cwd": "?", "logpath": None} for p in pids}
    try:
        result = subprocess.run(
            ["lsof", "-p", pid_args],
            capture_output=True,
            text=True,
            timeout=5,
        )
        for line in result.stdout.split("\n"):
            parts = line.split()
            if len(parts) < 9:
                continue
            try:
                pid = int(parts[1])
            except ValueError:
                continue
            if pid not in result_map:
                continue
            fd_col = parts[3]
            path = parts[-1]
            if fd_col == "cwd":
                result_map[pid]["cwd"] = path
            # opencode writes logs to ~/.local/share/opencode/log/<timestamp>.log
            # even after unlink, lsof reports the original path
            if ".log" in path and "opencode/log/" in path:
                result_map[pid]["logpath"] = path
    except (subprocess.TimeoutExpired, FileNotFoundError):
        pass
    return result_map


def get_opencode_processes():
    """find all running opencode processes via ps + single batched lsof call.

    filters: only processes whose binary basename is literally "opencode",
    excluding this script (opencode-htop) and grep artifacts.
    """
    raw = []
    try:
        result = subprocess.run(
            ["ps", "axo", "pid,pcpu,rss,tty,etime,args"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        for line in result.stdout.strip().split("\n")[1:]:
            parts = line.split(None, 5)
            if len(parts) < 6:
                continue
            pid_str, cpu_str, rss_str, tty, elapsed, args = parts
            if "opencode" not in args:
                continue
            if "opencode-htop" in args or "grep" in args:
                continue
            # verify the actual binary name, not just a substring match
            args_base = os.path.basename(args.split()[0]) if args.split() else ""
            if args_base != "opencode":
                continue
            raw.append(
                (
                    int(pid_str),
                    float(cpu_str),
                    int(rss_str),
                    tty,
                    elapsed.strip(),
                    args.strip(),
                )
            )
    except (subprocess.TimeoutExpired, FileNotFoundError):
        return []

    # one lsof call for all PIDs — extracts cwd and log file path
    pids = [r[0] for r in raw]
    lsof = _batch_lsof(pids)

    processes = []
    for pid, cpu, rss, tty, elapsed, args in raw:
        info = lsof.get(pid, {"cwd": "?", "logpath": None})

        # tier 1: parse explicit -s <session_id> from cmdline
        session_id = None
        session_match = re.search(r"(?:^|\s)-s\s+(ses_\S+)", args)
        if session_match:
            session_id = session_match.group(1)

        # tier 2 prep: extract process start time from log filename (UTC)
        start_ms = _parse_log_timestamp(info["logpath"]) if info["logpath"] else 0

        # detect `opencode run` tool processes (LSPs, one-shot command wrappers).
        # these are never interactive sessions — they're child processes spawned
        # by opencode to run language servers or execute one-shot prompts.
        args_parts = args.split()
        is_tool = len(args_parts) > 1 and args_parts[1] == "run"

        processes.append(
            ProcessInfo(
                pid=pid,
                cpu_percent=cpu,
                mem_mb=rss / 1024,
                elapsed=elapsed,
                tty=tty,
                cwd=info["cwd"],
                cmdline=args,
                session_id=session_id,
                start_time_ms=start_ms,
                is_tool_process=is_tool,
            )
        )
    return processes


def query_db(sql, params=()):
    """run a read-only query against the opencode sqlite db.

    uses ?mode=ro to open read-only — safe to query while opencode instances
    are actively writing (sqlite WAL mode allows concurrent readers).
    """
    if not DB_PATH.exists():
        return []
    try:
        conn = sqlite3.connect(
            f"file:{DB_PATH}?mode=ro&immutable=0",
            uri=True,
            timeout=2,
        )
        conn.row_factory = sqlite3.Row
        rows = conn.execute(sql, params).fetchall()
        conn.close()
        return rows
    except sqlite3.Error:
        return []


def get_session_info(session_id):
    """get full session info including message aggregates.

    token accounting: opencode splits tokens into input (uncached), cache.read,
    cache.write, and output. for display we compute:
      context = input + cache.read  (actual prompt size the model sees)
    raw "input" alone is misleadingly small (often single digits) because most
    prompt content comes from cache reads.
    """
    rows = query_db(
        """
        SELECT
            s.id, s.title, s.directory, s.project_id, s.version,
            s.permission,
            s.time_created as ses_created, s.time_updated,
            count(m.id) as msg_count,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN coalesce(json_extract(m.data, '$.tokens.input'), 0)
                   + coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
                ELSE 0 END) as total_context,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.tokens.output') ELSE 0 END) as total_output,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
                ELSE 0 END) as total_cache,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.cost') ELSE 0 END) as total_cost
        FROM session s
        LEFT JOIN message m ON m.session_id = s.id
        WHERE s.id = ?
        GROUP BY s.id
    """,
        (session_id,),
    )
    if not rows:
        return None

    row = rows[0]

    # most recent message determines current state (role, finish status, model)
    last_msg = query_db(
        """
        SELECT
            json_extract(data, '$.role') as role,
            json_extract(data, '$.finish') as finish,
            json_extract(data, '$.modelID') as model,
            json_extract(data, '$.agent') as agent,
            time_created
        FROM message
        WHERE session_id = ?
        ORDER BY time_created DESC
        LIMIT 1
    """,
        (session_id,),
    )

    last_role = last_msg[0]["role"] if last_msg else "?"
    last_finish = last_msg[0]["finish"] if last_msg else None
    last_model = last_msg[0]["model"] if last_msg else "?"
    last_agent = last_msg[0]["agent"] if last_msg else "?"
    last_time = last_msg[0]["time_created"] if last_msg else 0

    # round start = most recent user message. a "round" is one user prompt
    # plus all the assistant responses / tool calls that follow it.
    round_msg = query_db(
        """
        SELECT time_created
        FROM message
        WHERE session_id = ?
          AND json_extract(data, '$.role') = 'user'
        ORDER BY time_created DESC
        LIMIT 1
    """,
        (session_id,),
    )
    round_start = round_msg[0]["time_created"] if round_msg else 0

    # last output: the last non-empty line from the most recent assistant text
    # part. this populates the LAST column in the grid, giving a quick glance
    # at what the model is saying/just said.
    #
    # the data model: opencode stores message content in the `part` table, not
    # in the message itself. parts have a JSON `data` field with a `type`
    # discriminator: "text" (actual text), "tool" (tool call/result),
    # "step-start", "step-finish". we only want "text" parts from assistant
    # messages. we take the last non-empty line since the model's final line
    # of output is usually the most informative snippet.
    last_output_text = ""
    last_part = query_db(
        """
        SELECT p.data
        FROM part p
        JOIN message m ON p.message_id = m.id
        WHERE p.session_id = ?
          AND json_extract(m.data, '$.role') = 'assistant'
          AND json_extract(p.data, '$.type') = 'text'
        ORDER BY p.time_created DESC
        LIMIT 1
    """,
        (session_id,),
    )
    if last_part:
        try:
            part_data = json.loads(last_part[0]["data"])
            text = part_data.get("text", "")
            lines = [l.strip() for l in text.strip().splitlines() if l.strip()]
            last_output_text = lines[-1] if lines else ""
        except (ValueError, KeyError):
            pass

    # todos: the full list for this session (displayed in the 't' panel)
    todo_rows = query_db(
        """
        SELECT content, status, priority
        FROM todo
        WHERE session_id = ?
        ORDER BY position
    """,
        (session_id,),
    )

    todos = [TodoItem(t["content"], t["status"], t["priority"]) for t in todo_rows]

    return SessionInfo(
        session_id=session_id,
        title=row["title"] or "(untitled)",
        directory=row["directory"],
        project_id=row["project_id"],
        model=last_model or "?",
        agent=last_agent or "?",
        message_count=row["msg_count"] or 0,
        total_input_tokens=row["total_context"] or 0,
        total_output_tokens=row["total_output"] or 0,
        total_cache_read=row["total_cache"] or 0,
        total_cost=row["total_cost"] or 0,
        last_finish=last_finish,
        last_message_role=last_role,
        last_message_time=last_time,
        time_created=row["ses_created"],
        time_updated=row["time_updated"],
        round_start_time=round_start,
        last_output=last_output_text,
        active_todos=todos,
        version=row["version"] or "",
        interactive=row["permission"] is None,
    )


def find_session_for_process(proc, claimed_session_ids=None):
    """correlate a process to its most likely session (tiers 2 and 3).

    tier 1 (explicit -s flag) is handled before this function is called.

    tier 2: query for sessions matching process cwd that have message activity
    after the process start time. rank by message count — more messages means
    stronger correlation. skip sessions already claimed by other processes.

    tier 3: fallback to most recently updated session for the cwd, unclaimed.

    the claimed_session_ids set is critical for disambiguation. without it,
    two processes sharing cwd=~/ would both resolve to the same session. with
    it, the first process (by start time) claims the best match, and the second
    falls through to the next candidate. see refresh_data() for the two-pass
    algorithm that drives this.
    """
    if proc.session_id:
        return proc.session_id

    if not proc.cwd or proc.cwd == "?":
        return None

    claimed = claimed_session_ids or set()

    # tier 2: message-activity-since-start correlation
    if proc.start_time_ms > 0:
        rows = query_db(
            """
            SELECT s.id, count(m.id) as msgs_since
            FROM session s
            JOIN message m ON m.session_id = s.id
            WHERE s.directory = ?
              AND m.time_created >= ?
            GROUP BY s.id
            ORDER BY msgs_since DESC
            LIMIT 5
        """,
            (proc.cwd, proc.start_time_ms),
        )
        for row in rows:
            if row["id"] not in claimed:
                return row["id"]

    # tier 3: most recently updated session for this directory
    rows = query_db(
        """
        SELECT id FROM session
        WHERE directory = ?
        ORDER BY time_updated DESC
        LIMIT 5
    """,
        (proc.cwd,),
    )
    for row in rows:
        if row["id"] not in claimed:
            return row["id"]

    return None


def get_global_stats():
    """aggregate stats across all sessions (all time)."""
    rows = query_db("""
        SELECT
            count(DISTINCT s.id) as session_count,
            count(m.id) as message_count,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.tokens.input') ELSE 0 END) as total_input,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.tokens.output') ELSE 0 END) as total_output,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.cost') ELSE 0 END) as total_cost
        FROM session s
        LEFT JOIN message m ON m.session_id = s.id
    """)
    if rows:
        return dict(rows[0])
    return {}


def get_today_stats():
    """aggregate stats for sessions active today (local midnight cutoff)."""
    import datetime

    today = datetime.datetime.now().replace(hour=0, minute=0, second=0, microsecond=0)
    today_ms = int(today.timestamp() * 1000)

    rows = query_db(
        """
        SELECT
            count(DISTINCT s.id) as session_count,
            count(m.id) as message_count,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN coalesce(json_extract(m.data, '$.tokens.input'), 0)
                   + coalesce(json_extract(m.data, '$.tokens.cache.read'), 0)
                ELSE 0 END) as total_input,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.tokens.output') ELSE 0 END) as total_output,
            sum(CASE WHEN json_extract(m.data, '$.role') = 'assistant'
                THEN json_extract(m.data, '$.cost') ELSE 0 END) as total_cost
        FROM session s
        LEFT JOIN message m ON m.session_id = s.id
        WHERE s.time_updated > ?
    """,
        (today_ms,),
    )
    if rows:
        return dict(rows[0])
    return {}


def get_mcp_config():
    """read MCP server config from the global opencode.json.

    NOTE: only reads ~/.config/opencode/opencode.json (global config).
    per-project opencode.json files in project roots are not read yet.
    the mcp block maps server names to config objects with at minimum
    a `type` (local/remote), `command` or `url`, and `enabled` bool.
    """
    if not CONFIG_PATH.exists():
        return {}
    try:
        with open(CONFIG_PATH) as f:
            config = json.load(f)
        return config.get("mcp", {})
    except (json.JSONDecodeError, OSError):
        return {}


# -- detail view data --
#
# the detail view was the last major feature added. the insight: since we
# already extract each process's TTY from ps, and the user is running in
# tmux, we can map TTY -> tmux pane -> `tmux capture-pane` to get the
# actual live terminal content of any opencode session. this is more useful
# than just showing DB messages because it includes the full TUI state
# (tool outputs, progress, opencode's own chrome, etc.)
#
# for non-tmux environments, we fall back to querying the DB's message and
# part tables for a chronological message history.


def _tmux_pane_for_tty(tty):
    """map a TTY name (e.g. 'ttys005') to a tmux pane target (e.g. 'personal:3.1').

    runs `tmux list-panes -a` once and builds a tty -> pane lookup.
    returns None if tmux isn't running or the tty isn't in any pane.
    """
    try:
        out = subprocess.run(
            [
                "tmux",
                "list-panes",
                "-a",
                "-F",
                "#{pane_tty} #{session_name}:#{window_index}.#{pane_index}",
            ],
            capture_output=True,
            text=True,
            timeout=2,
        )
        if out.returncode != 0:
            return None
        # build lookup: /dev/ttys005 -> personal:3.1
        dev_tty = "/dev/%(tty)s" % {"tty": tty}
        for line in out.stdout.strip().splitlines():
            parts = line.split(" ", 1)
            if len(parts) == 2 and parts[0] == dev_tty:
                return parts[1]
    except (FileNotFoundError, subprocess.TimeoutExpired):
        pass
    return None


def capture_tmux_pane(tty):
    """capture the current screen content of a tmux pane by its TTY.

    returns a list of strings (screen lines), or None if capture fails.
    this gives us the actual live terminal output of the opencode process,
    including its TUI chrome, without needing to read from /dev/ttyXXX.
    """
    target = _tmux_pane_for_tty(tty)
    if not target:
        return None
    try:
        out = subprocess.run(
            ["tmux", "capture-pane", "-t", target, "-p"],
            capture_output=True,
            text=True,
            timeout=2,
        )
        if out.returncode == 0:
            return out.stdout.splitlines()
    except (FileNotFoundError, subprocess.TimeoutExpired):
        pass
    return None


def get_recent_messages(session_id, limit=30):
    """fetch recent messages for the detail view (DB fallback when tmux unavailable).

    returns a list of dicts with role, finish, model, tokens, cost, time, and
    the first text part content (if any). ordered oldest-first for display.
    """
    rows = query_db(
        """
        SELECT data, time_created
        FROM message
        WHERE session_id = ?
        ORDER BY time_created DESC
        LIMIT ?
    """,
        (session_id, limit),
    )

    messages = []
    for r in rows:
        try:
            d = json.loads(r["data"])
        except (ValueError, KeyError):
            continue
        messages.append(
            {
                "role": d.get("role", "?"),
                "finish": d.get("finish", ""),
                "model": d.get("modelID", ""),
                "tokens_in": d.get("tokens", {}).get("input", 0) or 0,
                "tokens_out": d.get("tokens", {}).get("output", 0) or 0,
                "cache_read": d.get("tokens", {}).get("cache", {}).get("read", 0) or 0,
                "cost": d.get("cost", 0) or 0,
                "time_created": r["time_created"],
            }
        )

    # reverse so oldest is first (chronological display)
    messages.reverse()

    # for each message, fetch the first text part to show a preview
    for msg in messages:
        # we don't have message_id directly, but can correlate by session+time
        parts = query_db(
            """
            SELECT p.data FROM part p
            JOIN message m ON p.message_id = m.id
            WHERE p.session_id = ?
              AND m.time_created = ?
              AND json_extract(p.data, '$.type') = 'text'
            ORDER BY p.time_created ASC
            LIMIT 1
        """,
            (session_id, msg["time_created"]),
        )
        if parts:
            try:
                pdata = json.loads(parts[0]["data"])
                msg["text_preview"] = pdata.get("text", "")[:200]
            except (ValueError, KeyError):
                msg["text_preview"] = ""
        else:
            msg["text_preview"] = ""

    return messages


# -- formatting helpers --
#
# all formatting uses %(name)s style string formatting per the project's
# python style guide (no f-strings, no .format()). this is a deliberate
# choice for consistency with the CLAUDE.md coding standards.


def format_tokens(n):
    """format token count with K/M suffixes."""
    if n is None:
        return "0"
    if n >= 1_000_000:
        return "%(value).1fM" % {"value": n / 1_000_000}
    if n >= 1_000:
        return "%(value).1fK" % {"value": n / 1_000}
    return str(int(n))


def format_cost(c):
    """format cost as dollars. most providers report 0 in the db currently."""
    if c is None or c == 0:
        return "-"
    return "$%(cost).2f" % {"cost": c}


def format_duration(ms):
    """format a millisecond duration as a compact human string.

    examples: "12s", "3m24s", "1h05m", "2d3h"
    returns "-" for zero/negative.
    """
    if ms <= 0:
        return "-"
    secs = int(ms / 1000)
    if secs < 60:
        return "%(s)ds" % {"s": secs}
    mins = secs // 60
    secs = secs % 60
    if mins < 60:
        return "%(m)dm%(s)02ds" % {"m": mins, "s": secs}
    hours = mins // 60
    mins = mins % 60
    if hours < 24:
        return "%(h)dh%(m)02dm" % {"h": hours, "m": mins}
    days = hours // 24
    hours = hours % 24
    return "%(d)dd%(h)dh" % {"d": days, "h": hours}


def infer_status(session, cpu_percent=0.0):
    """infer what the session is currently doing.

    primary signal: the `finish` field on the last assistant message in the db.
      - null/""       → streaming (model actively generating)
      - "tool-calls"  → model requested tool execution
      - "stop"        → model finished, waiting for user ("open")
      - "length"      → hit output limit, truncated

    secondary signal: process CPU% from ps. when the db looks idle but the
    process is burning CPU (>5%), something is happening that hasn't been
    committed to the db yet. this catches:
      - long-running bash commands during tool execution
      - model responses mid-stream (db writes are batched)
      - MCP server interactions

    status names:
      generating — model actively generating tokens
      tool use   — model requested tools, opencode executing them
      busy       — CPU hot but db stale (mid-response, hasn't flushed)
      thinking   — user just sent a message, model starting up
      queued     — last msg was user, been a while, model may be queued
      idle       — model finished, waiting for user input
      stale      — no finish, old, no CPU activity
      truncated  — hit output token limit
    """
    now_ms = int(time.time() * 1000)
    age_seconds = (
        (now_ms - session.last_message_time) / 1000
        if session.last_message_time
        else 9999
    )
    cpu_active = cpu_percent > 5.0

    if session.last_message_role == "assistant":
        if session.last_finish is None or session.last_finish == "":
            if age_seconds < 120:
                return "generating"
            if cpu_active:
                return "busy"
            return "stale"
        if session.last_finish == "tool-calls":
            if age_seconds < 30:
                return "tool use"
            if cpu_active:
                return "busy"
            return "idle"
        if session.last_finish == "stop":
            if cpu_active:
                return "busy"
            return "idle"
        if session.last_finish == "length":
            return "truncated"
        return "idle"
    if session.last_message_role == "user":
        if cpu_active:
            return "thinking"
        if age_seconds < 60:
            return "thinking"
        return "queued"
    return "unknown"


def status_color(status):
    """map status to curses color pair index.

    green: actively doing something (generating, tool use, busy)
    yellow: transitional (thinking, queued)
    white: idle (waiting for user input)
    dim: stale or unknown
    red: error states (truncated)
    """
    mapping = {
        "generating": 2,  # green
        "tool use": 2,  # green
        "busy": 2,  # green
        "thinking": 3,  # yellow
        "queued": 3,  # yellow
        "idle": 4,  # white
        "stale": 5,  # dim
        "truncated": 1,  # red
        "unknown": 5,  # dim
    }
    return mapping.get(status, 4)


def short_path(path, max_len=30):
    """abbreviate a path, replacing home with ~ and trimming from the left."""
    home = str(Path.home())
    if path.startswith(home):
        path = "~" + path[len(home) :]
    if len(path) <= max_len:
        return path
    return "..." + path[-(max_len - 3) :]


def short_sid(session_id):
    """abbreviate session ID for compact display contexts.

    NOTE: this function is no longer used in the main grid (which now shows
    full SIDs), but is kept for potential use in the detail view breadcrumb
    or other space-constrained contexts. originally used when the grid was
    single-row and needed a 10-char SID column. testing revealed that
    truncated SIDs can't actually be passed to `opencode -s`, so the grid
    was changed to show full 30-char IDs.
    """
    if not session_id:
        return "-"
    short = session_id.replace("ses_", "", 1)
    return short[:8]


def short_model(model):
    """abbreviate model names to fit in a 16-char column."""
    if not model or model == "?":
        return "?"
    # NOTE: ordered so longer/more-specific patterns match first where needed
    replacements = [
        ("claude-opus-4-5-20251101", "opus-4.5"),
        ("claude-sonnet-4-5-20250929", "sonnet-4.5"),
        ("claude-opus-4-6", "opus-4.6"),
        ("claude-sonnet-4-6", "sonnet-4.6"),
        ("claude-opus-4-5", "opus-4.5"),
        ("claude-sonnet-4-5", "sonnet-4.5"),
        ("gpt-5.2-codex", "gpt-5.2"),
        ("gpt-4o-mini", "4o-mini"),
        ("antigravity-", "ag/"),
        ("gemini-3-pro", "gem-3p"),
        ("gemini-3-flash", "gem-3f"),
    ]
    for old, new in replacements:
        model = model.replace(old, new)
    return model[:16]


# -- column definitions for sorting --
#
# each column has a key (used in sort) and a label (shown in the stats bar's
# "sort:LABEL dir" indicator). '>' cycles through these like htop's sort
# column picker.
#
# originally these also had "width" fields used for the single-row layout's
# column headers. removed when the grid moved to the two-row columnar design
# where column widths are computed directly in draw_session_table.
#
# the order here determines the cycling order when pressing '>'. STATUS is
# first because it's the most useful default sort (groups running sessions
# together). the rest follow the visual left-to-right column order in the grid.
#
# "cost" was removed from the sortable columns (and the grid entirely) because
# opencode's cost tracking is unreliable — most providers report 0 in the db,
# and claude discount tiers make the number meaningless anyway.

COLUMNS = [
    {"key": "status", "label": "STATUS"},
    {"key": "title", "label": "TITLE"},
    {"key": "last", "label": "LAST OUTPUT"},
    {"key": "msgs", "label": "MSGS"},
    {"key": "sid", "label": "SID"},
    {"key": "pid", "label": "PID"},
    {"key": "uptime", "label": "UPTIME"},
    {"key": "round", "label": "ROUND"},
    {"key": "cpu", "label": "CPU%"},
    {"key": "mem", "label": "MEM"},
    {"key": "tokens", "label": "CTX/OUT"},
    {"key": "model", "label": "MODEL"},
    {"key": "tty", "label": "TTY"},
]


def _sort_value(key, proc, session):
    """extract a sortable value for a given column key.

    returns a tuple (has_session, primary_value, title) so that:
      - unresolved processes always sort to the bottom (has_session=1)
      - within the same primary sort, sessions are stable by title

    the title secondary key is critical for UX: without it, sessions with the
    same status would constantly swap positions as their CPU% fluctuated
    between refreshes (e.g. two "open" sessions bouncing every 2 seconds).
    the user specifically requested this after observing the bounce behavior
    with the original CPU%-based default sort.
    """
    if not session:
        return (1, 0, "")  # push no-session rows to the bottom
    now_ms = int(time.time() * 1000)
    title = session.title.lower()
    mapping = {
        "pid": proc.pid,
        "sid": session.session_id,
        "tty": proc.tty,
        "cpu": proc.cpu_percent,
        "mem": proc.mem_mb,
        "status": infer_status(session, cpu_percent=proc.cpu_percent),
        "model": session.model or "",
        "msgs": session.message_count,
        "tokens": session.total_input_tokens,
        "title": session.title.lower(),
        "uptime": proc.start_time_ms if proc.start_time_ms > 0 else now_ms,
        "round": now_ms - session.round_start_time
        if session.round_start_time > 0
        else 0,
        "last": session.last_output,
    }
    return (0, mapping.get(key, 0), title)


# -- curses TUI --
#
# design philosophy: k9s-inspired. breadcrumb header, stats bar, keybind footer.
# the main innovation is the two-row columnar grid where each session takes two
# screen rows with vertically-paired values (title/last, status/msgs, etc.)
#
# the grid went through three layout iterations (see module docstring) before
# landing on the current columnar design where each "cell" has a top and bottom
# value, both labeled in a two-line header row. this was the user's original
# intent — the A/B pairs they specified were meant to be vertically stacked in
# the same column, not horizontally spread across two separate row purposes.
#
# color pairs:
#   1: red (error states: truncated)
#   2: green (active: streaming, tool-call, working)
#   3: yellow (transitional: responding, waiting)
#   4: white (open: session idle, waiting for user)
#   5: dim gray (secondary info: row 2 values, stats bar, separators)
#   6: cyan (breadcrumb header)
#   7: magenta (panel titles: TODOS, MCP SERVERS)
#   8: black-on-cyan (selection highlight — both rows of selected session)
#   9: black-on-yellow (sort column indicator in the header)


class OpenCodeTop:
    def __init__(self, stdscr):
        self.stdscr = stdscr
        self.scroll_offset = 0
        self.show_todos = False
        self.show_mcps = False
        self.selected_idx = 0
        self.last_refresh = 0
        self.processes = []
        self.sessions = []  # list of (ProcessInfo, SessionInfo|None)
        self.today_stats = {}
        self.global_stats = {}
        self.mcp_config = {}
        # sort state: column key and direction. '>' cycles forward, '<' cycles back.
        # default: sort by STATUS (index 0). title is used as a secondary sort
        # key for stability — within the same status, sessions stay alphabetical
        # and don't bounce around as CPU% fluctuates between refreshes.
        self.sort_col_idx = 0  # STATUS column
        self.sort_reverse = False  # ascending: "open" before "working" (alphabetical)
        # filter state: '/' opens filter input, esc clears it (k9s-style)
        self.filter_text = ""
        self.filter_active = False  # True while typing in the filter bar
        # visibility toggles (both default to hiding noise):
        #   'p' — tool processes (`opencode run` LSPs, one-shot wrappers)
        #   'a' — non-interactive sessions (commit-msg, subagents, Rose TTS, etc.)
        self.show_all_processes = False
        self.show_all_sessions = False
        # detail view state: enter on a row opens the detail view for that session.
        # shows tmux pane capture (live terminal) if available, DB messages otherwise.
        self.detail_mode = False
        self.detail_scroll = 0
        self.detail_lines = []  # cached screen lines for the detail view
        self.detail_session = None  # (proc, session) tuple for the detail view
        self.detail_source = ""  # "tmux" or "db" — which data source is showing
        # flash message: brief feedback shown in footer (e.g. after yank)
        self.flash_message = ""
        self.flash_time = 0

    def setup_colors(self):
        """initialize curses color pairs.

        pairs 1-7 have transparent backgrounds (-1) so they work on any terminal
        theme. pairs 8-9 have solid backgrounds for highlighting.

        NOTE: pairs 8 and 9 were originally uninitialized (a bug from the first
        TUI implementation). curses silently uses pair 0 (default) for
        uninitialized pairs, so the "selection highlight" was invisible until
        this was caught during the smoke test.
        """
        curses.start_color()
        curses.use_default_colors()
        curses.init_pair(1, curses.COLOR_RED, -1)  # error states
        curses.init_pair(2, curses.COLOR_GREEN, -1)  # active states
        curses.init_pair(3, curses.COLOR_YELLOW, -1)  # transitional states
        curses.init_pair(4, curses.COLOR_WHITE, -1)  # idle / open
        curses.init_pair(5, 8, -1)  # dim gray (color 8 = bright black)
        curses.init_pair(6, curses.COLOR_CYAN, -1)  # breadcrumb header
        curses.init_pair(7, curses.COLOR_MAGENTA, -1)  # panel titles
        curses.init_pair(
            8, curses.COLOR_BLACK, curses.COLOR_CYAN
        )  # selection highlight
        curses.init_pair(9, curses.COLOR_BLACK, curses.COLOR_YELLOW)  # sort indicator

    def refresh_data(self):
        """collect all data and resolve PID-to-session mappings.

        two-pass claimed-set algorithm for disambiguation:
          pass 1: processes with explicit -s flags claim their sessions.
          pass 2: remaining processes sorted by start_time ascending. each
                  claims the best unclaimed session via find_session_for_process.
                  sorting by start time ensures the older process (more message
                  history) gets first pick, and newer processes get the leftovers.
        """
        self.processes = get_opencode_processes()
        self.today_stats = get_today_stats()
        self.global_stats = get_global_stats()
        self.mcp_config = get_mcp_config()

        claimed = set()
        resolved = {}

        # pass 1: explicit session IDs from cmdline -s flag (skip tool processes)
        for proc in self.processes:
            if proc.session_id and not proc.is_tool_process:
                claimed.add(proc.session_id)
                resolved[proc.pid] = proc.session_id

        # pass 2: inferred matches, oldest process first (skip tool processes —
        # they'd steal session matches from real interactive processes)
        remaining = [
            p for p in self.processes if p.pid not in resolved and not p.is_tool_process
        ]
        remaining.sort(key=lambda p: p.start_time_ms)
        for proc in remaining:
            sid = find_session_for_process(proc, claimed_session_ids=claimed)
            if sid:
                claimed.add(sid)
                resolved[proc.pid] = sid

        # build final (proc, session_info) pairs preserving original order
        self.sessions = []
        for proc in self.processes:
            sid = resolved.get(proc.pid)
            info = get_session_info(sid) if sid else None
            self.sessions.append((proc, info))

        self.last_refresh = time.time()

    def _get_visible_sessions(self):
        """apply filter + sort to self.sessions, return the displayable list."""
        filtered = self.sessions
        # hide tool processes and no-session rows unless toggled on (p key)
        if not self.show_all_processes:
            filtered = [
                (p, s) for p, s in filtered if not p.is_tool_process and s is not None
            ]
        # hide non-interactive sessions (commit-msg, subagents, etc.) unless toggled on (a key)
        if not self.show_all_sessions:
            filtered = [(p, s) for p, s in filtered if s is None or s.interactive]
        if self.filter_text:
            needle = self.filter_text.lower()
            filtered = [
                (p, s)
                for p, s in filtered
                if needle in (s.title.lower() if s else "")
                or needle in (s.model.lower() if s else "")
                or needle in (s.session_id.lower() if s else "")
                or needle in p.cwd.lower()
                or needle in p.tty.lower()
                or needle
                in (infer_status(s, cpu_percent=p.cpu_percent).lower() if s else "")
            ]
        sort_key = COLUMNS[self.sort_col_idx]["key"]
        filtered.sort(
            key=lambda pair: _sort_value(sort_key, pair[0], pair[1]),
            reverse=self.sort_reverse,
        )
        return filtered

    def draw_header(self, y):
        """k9s-inspired header: context breadcrumb line + stats bar."""
        h, w = self.stdscr.getmaxyx()
        now = time.strftime("%H:%M:%S")
        active_count = sum(
            1 for p, s in self.sessions if s is not None and not p.is_tool_process
        )
        tool_count = sum(1 for p, _ in self.sessions if p.is_tool_process)
        running_label = "%(n)d active" % {"n": active_count}
        if tool_count > 0:
            running_label += " (+%(n)d bg)" % {"n": tool_count}

        total_sessions = self.global_stats.get("session_count", 0) or 0
        today_sessions = self.today_stats.get("session_count", 0) or 0
        today_msgs = self.today_stats.get("message_count", 0) or 0
        today_input = self.today_stats.get("total_input", 0) or 0
        today_output = self.today_stats.get("total_output", 0) or 0
        # line 1: breadcrumb — context > resource > filter
        crumb = " opencode > sessions"
        if self.filter_text:
            crumb += " > /%(filter)s" % {"filter": self.filter_text}
        right = "%(now)s " % {"now": now}
        padding = w - len(crumb) - len(right)
        if padding > 0:
            line1 = crumb + " " * padding + right
        else:
            line1 = (crumb + "  " + right)[:w]
        self.safe_addstr(y, 0, " " * w, curses.color_pair(6) | curses.A_BOLD)
        self.safe_addstr(y, 0, line1[:w], curses.color_pair(6) | curses.A_BOLD)

        # line 2: stats bar
        sort_label = COLUMNS[self.sort_col_idx]["label"]
        sort_dir = "desc" if self.sort_reverse else "asc"
        stats = (
            " %(running)s  "
            "%(today_sessions)d/%(total_sessions)d sessions  "
            "%(today_msgs)d msgs  "
            "ctx:%(today_input)s out:%(today_output)s"
            "  sort:%(sort)s %(dir)s"
        ) % {
            "running": running_label,
            "today_sessions": today_sessions,
            "total_sessions": total_sessions,
            "today_msgs": today_msgs,
            "today_input": format_tokens(today_input),
            "today_output": format_tokens(today_output),
            "sort": sort_label,
            "dir": sort_dir,
        }
        self.safe_addstr(y + 1, 0, stats[:w], curses.color_pair(5))

        return y + 2

    def draw_session_table(self, start_y):
        """columnar two-row-per-session grid with headers.

        each session occupies two screen rows. columns are vertically paired
        so related values stack:

          TITLE       STATUS  SID                             UP      CPU   CTX     MODEL
          <title>     open    ses_38580cc10ffeqMDjXZixR9Ix3S  5h29m   0.9%  14.8M   opus-4.6
          <last out>  165     77988                           33m43s  144M  84.8K   ttys005

        TITLE is flexible-width (fills remaining space). all other columns are
        fixed. the active sort column header gets a highlight in the header row.
        """
        h, w = self.stdscr.getmaxyx()
        now_ms = int(time.time() * 1000)

        # fixed column widths (right-side columns, drawn right-to-left from
        # the available width to give TITLE the remainder)
        # NOTE: these are content widths; each column gets a 2-char gap after it
        COL_STATUS = 10  # "generating" is the longest status (10 chars)
        COL_SID = 30  # session IDs are always 30 chars
        COL_UP = 8  # "12h34m" fits; "33m43s" fits
        COL_CPU = 6  # "25.5%" fits in 5, 1 extra for breathing room
        COL_CTX = 8  # "14.8M" / "84.8K" / "232.4K"
        COL_MODEL = 12  # "opus-4.6" / "sonnet-4.5"

        # gap between columns
        GAP = 2

        # total fixed width consumed by right-side columns + gaps + leading space
        fixed_total = (
            GAP  # leading indent
            + COL_STATUS
            + GAP
            + COL_SID
            + GAP
            + COL_UP
            + GAP
            + COL_CPU
            + GAP
            + COL_CTX
            + GAP
            + COL_MODEL
        )
        title_width = max(10, w - fixed_total - GAP)

        # column x-positions (left edge of each column's content area)
        x_title = GAP
        x_status = x_title + title_width + GAP
        x_sid = x_status + COL_STATUS + GAP
        x_up = x_sid + COL_SID + GAP
        x_cpu = x_up + COL_UP + GAP
        x_ctx = x_cpu + COL_CPU + GAP
        x_model = x_ctx + COL_CTX + GAP

        # header-to-sort-key mapping for highlight
        active_sort_key = COLUMNS[self.sort_col_idx]["key"]
        header_cols = [
            (x_title, title_width, "TITLE", "title"),
            (x_status, COL_STATUS, "STATUS", "status"),
            (x_sid, COL_SID, "SID", "sid"),
            (x_up, COL_UP, "UP", "uptime"),
            (x_cpu, COL_CPU, "CPU", "cpu"),
            (x_ctx, COL_CTX, "CTX", "tokens"),
            (x_model, COL_MODEL, "MODEL", "model"),
        ]
        header2_cols = [
            (x_title, title_width, "LAST", "last"),
            (x_status, COL_STATUS, "MSGS", "msgs"),
            (x_sid, COL_SID, "PID", "pid"),
            (x_up, COL_UP, "ROUND", "round"),
            (x_cpu, COL_CPU, "MEM", "mem"),
            (x_ctx, COL_CTX, "OUT", "tokens"),
            (x_model, COL_MODEL, "TTY", "tty"),
        ]

        # draw header row 1 (top labels)
        hdr_attr = curses.A_BOLD | curses.color_pair(5)
        hi_attr = curses.A_BOLD | curses.color_pair(9)
        self.safe_addstr(start_y, 0, " " * w, hdr_attr)
        for cx, cw, label, sort_key in header_cols:
            attr = hi_attr if sort_key == active_sort_key else hdr_attr
            self.safe_addstr(start_y, cx, label[:cw], attr)

        # draw header row 2 (bottom labels)
        self.safe_addstr(start_y + 1, 0, " " * w, hdr_attr)
        for cx, cw, label, sort_key in header2_cols:
            attr = hi_attr if sort_key == active_sort_key else hdr_attr
            self.safe_addstr(start_y + 1, cx, label[:cw], attr)

        # separator
        self.safe_addstr(start_y + 2, 0, ("─" * w)[:w], curses.color_pair(5))

        # 3 rows per session (2 data + 1 blank), plus header (2) + separator (1)
        content_start = start_y + 3
        footer_reserve = 3  # detail line + footer
        if self.show_todos or self.show_mcps:
            footer_reserve += 8
        available_sessions = max(1, (h - content_start - footer_reserve) // 3)

        visible = self._get_visible_sessions()
        page = visible[self.scroll_offset : self.scroll_offset + available_sessions]
        row_y = content_start

        for i, (proc, session) in enumerate(page):
            abs_idx = self.scroll_offset + i
            is_selected = abs_idx == self.selected_idx

            if session:
                status = infer_status(session, cpu_percent=proc.cpu_percent)
                sc = status_color(status)
                uptime_ms = now_ms - proc.start_time_ms if proc.start_time_ms > 0 else 0
                round_ms = (
                    now_ms - session.round_start_time
                    if session.round_start_time > 0
                    else 0
                )
                last_text = (session.last_output or "")[:title_width]

                r1_attr = curses.color_pair(8) if is_selected else curses.color_pair(sc)
                r2_attr = curses.color_pair(8) if is_selected else curses.color_pair(5)

                # paint both row backgrounds
                self.safe_addstr(row_y, 0, " " * w, r1_attr)
                self.safe_addstr(row_y + 1, 0, " " * w, r2_attr)

                # row 1: title, status, sid, uptime, cpu%, ctx, model
                self.safe_addstr(row_y, x_title, session.title[:title_width], r1_attr)
                self.safe_addstr(row_y, x_status, status[:COL_STATUS], r1_attr)
                self.safe_addstr(row_y, x_sid, session.session_id[:COL_SID], r1_attr)
                self.safe_addstr(
                    row_y, x_up, format_duration(uptime_ms)[:COL_UP], r1_attr
                )
                self.safe_addstr(
                    row_y,
                    x_cpu,
                    ("%(cpu).1f%%" % {"cpu": proc.cpu_percent})[:COL_CPU],
                    r1_attr,
                )
                self.safe_addstr(
                    row_y,
                    x_ctx,
                    format_tokens(session.total_input_tokens)[:COL_CTX],
                    r1_attr,
                )
                self.safe_addstr(
                    row_y, x_model, short_model(session.model)[:COL_MODEL], r1_attr
                )

                # row 2: last output, msgs, pid, round, mem, out, tty
                self.safe_addstr(row_y + 1, x_title, last_text, r2_attr)
                self.safe_addstr(
                    row_y + 1,
                    x_status,
                    str(session.message_count)[:COL_STATUS],
                    r2_attr,
                )
                self.safe_addstr(row_y + 1, x_sid, str(proc.pid)[:COL_SID], r2_attr)
                self.safe_addstr(
                    row_y + 1, x_up, format_duration(round_ms)[:COL_UP], r2_attr
                )
                self.safe_addstr(
                    row_y + 1,
                    x_cpu,
                    ("%(mem).0fM" % {"mem": proc.mem_mb})[:COL_CPU],
                    r2_attr,
                )
                self.safe_addstr(
                    row_y + 1,
                    x_ctx,
                    format_tokens(session.total_output_tokens)[:COL_CTX],
                    r2_attr,
                )
                self.safe_addstr(row_y + 1, x_model, proc.tty[:COL_MODEL], r2_attr)

            else:
                # no session matched
                r_attr = curses.color_pair(8) if is_selected else curses.color_pair(5)
                self.safe_addstr(row_y, 0, " " * w, r_attr)
                self.safe_addstr(row_y + 1, 0, " " * w, r_attr)
                self.safe_addstr(row_y, x_title, proc.cmdline[:title_width], r_attr)
                self.safe_addstr(row_y, x_status, "no-session", r_attr)
                self.safe_addstr(
                    row_y + 1,
                    x_title,
                    short_path(proc.cwd, max_len=title_width),
                    r_attr,
                )
                self.safe_addstr(row_y + 1, x_sid, str(proc.pid), r_attr)
                self.safe_addstr(row_y + 1, x_model, proc.tty, r_attr)

            row_y += 3  # 2 data rows + 1 blank spacer

        return row_y

    def draw_todos_panel(self, start_y):
        """draw the todo list for the currently selected session (toggle: t)."""
        h, w = self.stdscr.getmaxyx()
        if not self.show_todos:
            return start_y

        self.safe_addstr(start_y, 0, "─" * w, curses.color_pair(5))
        start_y += 1
        self.safe_addstr(
            start_y,
            0,
            " TODOS (selected session)",
            curses.A_BOLD | curses.color_pair(7),
        )
        start_y += 1

        visible = self._get_visible_sessions()
        if self.selected_idx < len(visible):
            _, session = visible[self.selected_idx]
            if session and session.active_todos:
                for todo in session.active_todos[:6]:
                    status_char = {
                        "completed": "x",
                        "in_progress": ">",
                        "pending": " ",
                        "cancelled": "-",
                    }.get(todo.status, "?")
                    priority_color = {"high": 1, "medium": 3, "low": 5}.get(
                        todo.priority, 4
                    )
                    line = " [%(s)s] %(c)s" % {"s": status_char, "c": todo.content}
                    self.safe_addstr(
                        start_y, 0, line[:w], curses.color_pair(priority_color)
                    )
                    start_y += 1
            else:
                self.safe_addstr(start_y, 0, "  (no todos)", curses.color_pair(5))
                start_y += 1

        return start_y

    def draw_mcps_panel(self, start_y):
        """draw the global MCP server config (toggle: m).

        reads from ~/.config/opencode/opencode.json only. MCPs with
        `enabled: false` are counted as disabled; everything else is enabled
        (opencode defaults enabled to true when the key is absent).
        """
        h, w = self.stdscr.getmaxyx()
        if not self.show_mcps:
            return start_y

        self.safe_addstr(start_y, 0, "─" * w, curses.color_pair(5))
        start_y += 1
        self.safe_addstr(
            start_y, 0, " MCP SERVERS", curses.A_BOLD | curses.color_pair(7)
        )
        start_y += 1

        if not self.mcp_config:
            self.safe_addstr(start_y, 0, "  (no config found)", curses.color_pair(5))
            return start_y + 1

        enabled = []
        disabled = []
        for name, cfg in sorted(self.mcp_config.items()):
            if isinstance(cfg, dict) and cfg.get("enabled", True):
                enabled.append(name)
            else:
                disabled.append(name)

        if enabled:
            line = "  enabled: " + ", ".join(enabled)
            self.safe_addstr(start_y, 0, line[:w], curses.color_pair(2))
            start_y += 1

        if disabled:
            line = "  disabled: %(count)d servers (%(names)s)" % {
                "count": len(disabled),
                "names": ", ".join(disabled[:5]) + ("..." if len(disabled) > 5 else ""),
            }
            self.safe_addstr(start_y, 0, line[:w], curses.color_pair(5))
            start_y += 1

        return start_y

    def draw_footer(self):
        """k9s-style footer: keybind hints on the bottom line."""
        h, w = self.stdscr.getmaxyx()
        if self.filter_active:
            # show filter input prompt
            prompt = " /%(text)s" % {"text": self.filter_text}
            self.safe_addstr(h - 1, 0, " " * w, curses.color_pair(6))
            self.safe_addstr(h - 1, 0, prompt[:w], curses.color_pair(6) | curses.A_BOLD)
            return

        # keybind bar — styled like k9s with key:action pairs
        binds = [
            ("q", "quit"),
            ("enter", "view"),
            ("r", "refresh"),
            ("y", "yank"),
            (">/<", "sort"),
            ("s", "flip"),
            ("/", "filter"),
            ("esc", "clear"),
            ("a", "sessions"),
            ("p", "procs"),
            ("t", "todos"),
            ("m", "mcps"),
            ("j/k", "scroll"),
        ]
        parts = []
        for key, action in binds:
            parts.append(" %(key)s:%(action)s" % {"key": key, "action": action})
        bar = " ".join(parts)
        self.safe_addstr(h - 1, 0, " " * w, curses.color_pair(5))
        self.safe_addstr(h - 1, 0, bar[:w], curses.color_pair(5))

        # flash message overlay (e.g. "yanked: ses_xxx") — shown for 1.5s
        if self.flash_message and time.time() - self.flash_time < 1.5:
            flash = " %(msg)s " % {"msg": self.flash_message}
            flash_x = max(0, w - len(flash))
            self.safe_addstr(
                h - 1, flash_x, flash[:w], curses.color_pair(2) | curses.A_BOLD
            )

    def draw_detail_line(self, y):
        """show cwd for the currently selected process."""
        h, w = self.stdscr.getmaxyx()
        visible = self._get_visible_sessions()
        if self.selected_idx >= len(visible):
            return y

        proc, session = visible[self.selected_idx]
        cwd_display = short_path(proc.cwd, max_len=w - 4)
        line = " %(cwd)s" % {"cwd": cwd_display}
        self.safe_addstr(y, 0, line[:w], curses.color_pair(5))
        return y + 1

    def refresh_detail_data(self):
        """refresh the detail view content for the currently selected session.

        tries tmux pane capture first (live terminal), falls back to DB messages.
        tmux capture is preferred because it shows the actual opencode TUI state
        including tool outputs, progress bars, etc. DB messages only show the
        message history without tool call details.
        """
        proc, session = self.detail_session
        if not proc:
            self.detail_lines = ["  (no process)"]
            self.detail_source = ""
            return

        # try tmux capture first — shows the live terminal screen
        pane_lines = capture_tmux_pane(proc.tty)
        if pane_lines is not None:
            self.detail_lines = pane_lines
            self.detail_source = "tmux"
            return

        # fallback: recent messages from the database
        if not session:
            self.detail_lines = ["  (no session data)"]
            self.detail_source = ""
            return

        msgs = get_recent_messages(session.session_id, limit=30)
        lines = []
        for msg in msgs:
            ts = ""
            if msg["time_created"]:
                ts = time.strftime(
                    "%H:%M:%S",
                    time.localtime(msg["time_created"] / 1000),
                )
            role = msg["role"]
            finish = msg["finish"] or ""
            tokens = ""
            if msg["tokens_out"] > 0:
                tokens = " ctx:%(i)s out:%(o)s" % {
                    "i": format_tokens(msg["tokens_in"] + msg["cache_read"]),
                    "o": format_tokens(msg["tokens_out"]),
                }
            header = " %(ts)s  %(role)-10s %(finish)-12s%(tokens)s" % {
                "ts": ts,
                "role": role,
                "finish": finish,
                "tokens": tokens,
            }
            lines.append(header)
            if msg["text_preview"]:
                # wrap preview text into indented lines
                preview = msg["text_preview"].replace("\n", " ")
                while preview:
                    chunk = preview[:76]
                    preview = preview[76:]
                    lines.append("            %(chunk)s" % {"chunk": chunk})
            lines.append("")  # blank separator between messages

        self.detail_lines = lines if lines else ["  (no messages)"]
        self.detail_source = "db"

    def draw_detail_view(self):
        """render the detail view (entered with Enter on a session row).

        two modes:
          - tmux: shows the live terminal screen captured from the tmux pane.
            this includes the opencode TUI chrome, tool outputs, etc.
          - db: shows recent messages from the database with role, timestamps,
            token usage, and text previews.

        scrollable with j/k. r refreshes the capture. esc/q goes back to list.
        """
        h, w = self.stdscr.getmaxyx()
        proc, session = self.detail_session

        # header line: breadcrumb showing which session we're looking at
        title = session.title if session else "(no session)"
        sid = session.session_id if session else "-"
        status = infer_status(session, cpu_percent=proc.cpu_percent) if session else "?"
        source_tag = (
            "[%(src)s]" % {"src": self.detail_source} if self.detail_source else ""
        )

        crumb = " opencode > sessions > %(sid)s %(source)s" % {
            "sid": sid,
            "source": source_tag,
        }
        right = "%(status)s " % {"status": status}
        padding = w - len(crumb) - len(right)
        if padding > 0:
            line1 = crumb + " " * padding + right
        else:
            line1 = (crumb + "  " + right)[:w]
        self.safe_addstr(0, 0, " " * w, curses.color_pair(6) | curses.A_BOLD)
        self.safe_addstr(0, 0, line1[:w], curses.color_pair(6) | curses.A_BOLD)

        # session info bar
        info_parts = []
        if session:
            info_parts.append("%(title)s" % {"title": title[:40]})
            info_parts.append("pid:%(pid)d" % {"pid": proc.pid})
            info_parts.append("tty:%(tty)s" % {"tty": proc.tty})
            info_parts.append(short_path(proc.cwd, max_len=30))
        info_line = " " + "  ".join(info_parts)
        self.safe_addstr(1, 0, info_line[:w], curses.color_pair(5))

        # separator
        self.safe_addstr(2, 0, ("─" * w)[:w], curses.color_pair(5))

        # content area: scrollable lines
        content_start = 3
        content_rows = h - content_start - 1  # leave room for footer
        visible_lines = self.detail_lines[
            self.detail_scroll : self.detail_scroll + content_rows
        ]
        for i, line in enumerate(visible_lines):
            y = content_start + i
            # dim gray for tmux output, normal for db messages
            attr = (
                curses.color_pair(4)
                if self.detail_source == "tmux"
                else curses.color_pair(4)
            )
            self.safe_addstr(y, 0, line[:w], attr)

        # footer with detail-mode keybinds
        binds = [
            ("esc", "back"),
            ("r", "refresh"),
            ("j/k", "scroll"),
            ("tab", "toggle tmux/db"),
        ]
        parts = []
        for key, action in binds:
            parts.append(" %(key)s:%(action)s" % {"key": key, "action": action})
        bar = " ".join(parts)
        self.safe_addstr(h - 1, 0, " " * w, curses.color_pair(5))
        self.safe_addstr(h - 1, 0, bar[:w], curses.color_pair(5))

    def safe_addstr(self, y, x, text, attr=0):
        """write to screen, silently ignoring out-of-bounds writes.

        curses raises an error when writing to the bottom-right cell of the
        screen; this wrapper swallows that and any other positioning errors.
        """
        h, w = self.stdscr.getmaxyx()
        if y < 0 or y >= h or x >= w:
            return
        try:
            self.stdscr.addnstr(y, x, text, w - x, attr)
        except curses.error:
            pass

    def _handle_filter_key(self, key):
        """process a keypress while the filter input is active.

        enter or esc commits/clears the filter and exits filter mode.
        backspace edits. printable chars append.
        """
        if key == 27:  # esc — clear filter and exit filter mode
            self.filter_text = ""
            self.filter_active = False
        elif key in (curses.KEY_ENTER, 10, 13):  # enter — commit filter
            self.filter_active = False
        elif key in (curses.KEY_BACKSPACE, 127, 8):  # backspace
            self.filter_text = self.filter_text[:-1]
        elif 32 <= key < 127:  # printable ascii
            self.filter_text += chr(key)

    def _enter_detail(self):
        """enter the detail view for the currently selected session."""
        visible = self._get_visible_sessions()
        if self.selected_idx >= len(visible):
            return
        self.detail_session = visible[self.selected_idx]
        self.detail_scroll = 0
        self.detail_mode = True
        self.refresh_detail_data()

    def _toggle_detail_source(self):
        """toggle between tmux and db views in detail mode.

        when switching to tmux, re-captures the pane. when switching to db,
        re-fetches messages. if tmux isn't available, stays on db.

        NOTE: the db message formatting here duplicates logic in _enter_detail().
        both build the same lines list from get_recent_messages(). factoring it
        out would need a shared _format_db_messages() helper. leaving as-is for
        now since it's only two call sites and the formatting may diverge (e.g.
        detail entry could show a richer initial view vs toggle refresh).
        """
        proc, session = self.detail_session
        if self.detail_source == "tmux":
            # switch to db view
            if session:
                msgs = get_recent_messages(session.session_id, limit=30)
                lines = []
                for msg in msgs:
                    ts = ""
                    if msg["time_created"]:
                        ts = time.strftime(
                            "%H:%M:%S",
                            time.localtime(msg["time_created"] / 1000),
                        )
                    role = msg["role"]
                    finish = msg["finish"] or ""
                    tokens = ""
                    if msg["tokens_out"] > 0:
                        tokens = " ctx:%(i)s out:%(o)s" % {
                            "i": format_tokens(msg["tokens_in"] + msg["cache_read"]),
                            "o": format_tokens(msg["tokens_out"]),
                        }
                    header = " %(ts)s  %(role)-10s %(finish)-12s%(tokens)s" % {
                        "ts": ts,
                        "role": role,
                        "finish": finish,
                        "tokens": tokens,
                    }
                    lines.append(header)
                    if msg["text_preview"]:
                        preview = msg["text_preview"].replace("\n", " ")
                        while preview:
                            lines.append(
                                "            %(chunk)s" % {"chunk": preview[:76]}
                            )
                            preview = preview[76:]
                    lines.append("")
                self.detail_lines = lines if lines else ["  (no messages)"]
                self.detail_source = "db"
        else:
            # switch to tmux view (re-capture)
            pane_lines = capture_tmux_pane(proc.tty)
            if pane_lines is not None:
                self.detail_lines = pane_lines
                self.detail_source = "tmux"
            # if tmux not available, stay on db
        self.detail_scroll = 0

    def run(self):
        """main event loop — two modes sharing one getch() timeout.

        the loop uses stdscr.timeout() so getch() returns -1 after
        REFRESH_INTERVAL seconds with no input. this drives both the
        auto-refresh cycle (data re-fetched every tick) and keeps the
        UI responsive without busy-waiting.

        detail mode and list mode are separate branches:
          - detail mode: auto-refreshes tmux capture on each tick for
            live terminal viewing. keybinds are local (scroll, tab to
            toggle source, q/esc to exit back to list).
          - list mode: refreshes process/session data on each tick.
            draws header → table → detail line → panels → footer.
            keybinds handle navigation, sorting, filtering, panels.

        filter mode is a sub-state within list mode: when active, all
        keystrokes go to _handle_filter_key (typing, backspace, enter
        to confirm, esc to cancel).
        """
        self.setup_colors()
        curses.curs_set(0)  # hide cursor
        self.stdscr.timeout(int(REFRESH_INTERVAL * 1000))

        while True:
            now = time.time()

            # -- detail mode --
            if self.detail_mode:
                # auto-refresh: re-capture tmux pane on each tick for live view
                if (
                    self.detail_source == "tmux"
                    and now - self.last_refresh >= REFRESH_INTERVAL
                ):
                    self.refresh_detail_data()
                    self.last_refresh = now

                self.stdscr.erase()
                self.draw_detail_view()
                self.stdscr.refresh()

                key = self.stdscr.getch()
                if key == 27 or key == ord("q"):  # esc or q: back to list
                    self.detail_mode = False
                    self.last_refresh = 0  # trigger data refresh on return
                if key == ord("r"):
                    self.refresh_detail_data()
                if key == ord("\t"):  # tab: toggle tmux/db
                    self._toggle_detail_source()
                if key in (ord("j"), curses.KEY_DOWN):
                    max_scroll = max(0, len(self.detail_lines) - 10)
                    self.detail_scroll = min(self.detail_scroll + 1, max_scroll)
                if key in (ord("k"), curses.KEY_UP):
                    self.detail_scroll = max(self.detail_scroll - 1, 0)
                # page down / page up for faster scrolling
                if key in (ord("d"), curses.KEY_NPAGE):
                    h, _ = self.stdscr.getmaxyx()
                    max_scroll = max(0, len(self.detail_lines) - 10)
                    self.detail_scroll = min(self.detail_scroll + h // 2, max_scroll)
                if key in (ord("u"), curses.KEY_PPAGE):
                    h, _ = self.stdscr.getmaxyx()
                    self.detail_scroll = max(self.detail_scroll - h // 2, 0)
                continue

            # -- list mode --
            if now - self.last_refresh >= REFRESH_INTERVAL:
                self.refresh_data()

            self.stdscr.erase()

            y = self.draw_header(0)
            y = self.draw_session_table(y)
            y = self.draw_detail_line(y)
            y = self.draw_todos_panel(y)
            y = self.draw_mcps_panel(y)
            self.draw_footer()

            self.stdscr.refresh()

            key = self.stdscr.getch()

            # when filter input is active, all keys go to the filter handler
            if self.filter_active:
                self._handle_filter_key(key)
                continue

            if key == ord("q"):
                break
            if key == ord("r"):
                self.last_refresh = 0
            if key == ord("t"):
                self.show_todos = not self.show_todos
            if key == ord("m"):
                self.show_mcps = not self.show_mcps
            if key == ord("a"):
                self.show_all_sessions = not self.show_all_sessions
            if key == ord("p"):
                self.show_all_processes = not self.show_all_processes

            # y: yank (pbcopy) the selected session's ID
            if key == ord("y"):
                visible = self._get_visible_sessions()
                if self.selected_idx < len(visible):
                    _, session = visible[self.selected_idx]
                    if session:
                        try:
                            subprocess.run(
                                ["pbcopy"],
                                input=session.session_id,
                                text=True,
                                timeout=2,
                            )
                            self.flash_message = "yanked: %(sid)s" % {
                                "sid": session.session_id
                            }
                            self.flash_time = time.time()
                        except (FileNotFoundError, subprocess.TimeoutExpired):
                            pass

            # enter: open detail view for selected session
            if key in (curses.KEY_ENTER, 10, 13):
                self._enter_detail()
                continue

            # '>' cycles sort column forward (like htop)
            if key == ord(">") or key == ord("."):
                self.sort_col_idx = (self.sort_col_idx + 1) % len(COLUMNS)
            # '<' cycles sort column backward
            if key == ord("<") or key == ord(","):
                self.sort_col_idx = (self.sort_col_idx - 1) % len(COLUMNS)
            # 's' flips sort direction
            if key == ord("s"):
                self.sort_reverse = not self.sort_reverse

            # '/' enters filter mode (k9s-style)
            if key == ord("/"):
                self.filter_active = True
                self.filter_text = ""
            # esc clears filter when not in filter mode
            if key == 27 and not self.filter_active:
                self.filter_text = ""

            # navigation
            visible = self._get_visible_sessions()
            max_idx = max(0, len(visible) - 1)
            if key in (ord("j"), curses.KEY_DOWN):
                self.selected_idx = min(self.selected_idx + 1, max_idx)
            if key in (ord("k"), curses.KEY_UP):
                self.selected_idx = max(self.selected_idx - 1, 0)
            # clamp selection after filter changes
            self.selected_idx = min(self.selected_idx, max_idx)

            # auto-scroll to keep selection visible (3 rows per session: 2 data + 1 spacer)
            h, _ = self.stdscr.getmaxyx()
            overhead = 10  # header, stats bar, separator, detail line, footer
            if self.show_todos or self.show_mcps:
                overhead += 8
            visible_sessions = max(1, (h - overhead) // 3)
            if self.selected_idx >= self.scroll_offset + visible_sessions:
                self.scroll_offset = self.selected_idx - visible_sessions + 1
            if self.selected_idx < self.scroll_offset:
                self.scroll_offset = self.selected_idx


def main(stdscr):
    app = OpenCodeTop(stdscr)
    app.run()


def _set_process_title():
    """best-effort process title for tmux automatic-rename and Activity Monitor.

    three approaches, none require pip packages:
      1. tmux escape: \033kopencode-htop\033\\ sets the tmux window name directly.
         this is what tmux actually reads for automatic-rename.
      2. terminal title: \033]2;opencode-htop\007 sets the xterm title string,
         which some terminal emulators and tmux configs also respect.
      3. pthread_setname_np: sets the macOS thread name visible in Activity
         Monitor and `ps -M`, though not in plain `ps` output.
    """
    import sys

    # tmux window name escape (most impactful for the user's use case)
    sys.stdout.write("\033kopencode-htop\033\\")
    # xterm title escape
    sys.stdout.write("\033]2;opencode-htop\007")
    sys.stdout.flush()

    # macOS pthread name (shows in Activity Monitor)
    try:
        import ctypes
        import ctypes.util

        libpthread_path = ctypes.util.find_library("pthread")
        if libpthread_path:
            libpthread = ctypes.cdll.LoadLibrary(libpthread_path)
            libpthread.pthread_setname_np(b"opencode-htop")
    except Exception:
        pass  # not critical


def _signal_exit(signum, _frame):
    """convert SIGTERM/SIGHUP into a clean SystemExit.

    curses.wrapper's finally block will call endwin() on SystemExit just
    like it does for KeyboardInterrupt, so the terminal gets restored
    properly instead of dumping a raw traceback with the alternate screen
    still active.
    """
    raise SystemExit(128 + signum)


def cli():
    """entry point for the `otop` console script (via pyproject.toml)."""
    if not DB_PATH.exists():
        print("error: opencode db not found at %(path)s" % {"path": DB_PATH})
        raise SystemExit(1)

    # register before curses init so signals during startup also exit cleanly
    signal.signal(signal.SIGTERM, _signal_exit)
    signal.signal(signal.SIGHUP, _signal_exit)

    _set_process_title()

    try:
        curses.wrapper(main)
    except KeyboardInterrupt:
        # ctrl-c: exit silently (curses.wrapper already called endwin)
        pass


if __name__ == "__main__":
    cli()

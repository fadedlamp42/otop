// opencode-otop: writes PID-to-session mapping files so otop can
// reliably correlate opencode processes to their active sessions.
//
// writes the current session ID to ~/.local/share/opencode/otop/<PID>
// and cleans up the file when the process exits.
//
// ses_34dda6ebdffev5A6J7sPKV6fVt

import type { Plugin, Hooks } from "@opencode-ai/plugin"
import { mkdirSync, writeFileSync, unlinkSync } from "fs"
import { join } from "path"
import { homedir } from "os"

const pidDir = join(
  process.env.XDG_DATA_HOME || join(homedir(), ".local", "share"),
  "opencode",
  "otop",
)
const pidFile = join(pidDir, String(process.pid))

function writePidFile(sessionID: string) {
  try {
    mkdirSync(pidDir, { recursive: true })
    writeFileSync(pidFile, sessionID)
  } catch {}
}

function removePidFile() {
  try {
    unlinkSync(pidFile)
  } catch {}
}

process.on("exit", removePidFile)
process.on("SIGINT", () => {
  removePidFile()
  process.exit(0)
})
process.on("SIGTERM", () => {
  removePidFile()
  process.exit(0)
})

// extracts a session ID from any event that carries one.
// session.created/updated store it at properties.info.id;
// most other events have it directly at properties.sessionID.
function extractSessionID(event: { type: string; properties: any }): string | undefined {
  const props = event.properties
  if (!props) return undefined
  if (
    (event.type === "session.created" || event.type === "session.updated") &&
    props.info?.id
  ) {
    return props.info.id
  }
  if (props.sessionID) return props.sessionID
  return undefined
}

export const OtopPlugin: Plugin = async (input) => {
  let resolved = false

  // check process.argv for an explicit `-s` session ID first.
  // when resuming a session (`c -s ses_XXX`), the ID is right there.
  const flagIndex = process.argv.indexOf("-s")
  if (flagIndex !== -1 && process.argv[flagIndex + 1]) {
    writePidFile(process.argv[flagIndex + 1])
    resolved = true
  }

  // for new sessions (no `-s`), eagerly try to resolve via session.list().
  // only writes if no event has resolved first, avoiding the race where
  // eagerResolve() overwrites a correct event-based value.
  if (!resolved) {
    const eagerResolve = async () => {
      try {
        if (resolved) return
        const response = await input.client.session.list()
        const sessions = response.data
        if (!resolved && sessions && sessions.length > 0) {
          const latestSession = sessions[sessions.length - 1]
          writePidFile(latestSession.id)
        }
      } catch {}
    }
    eagerResolve()
  }

  return {
    event: async ({ event }) => {
      const sessionID = extractSessionID(event)
      if (sessionID) {
        writePidFile(sessionID)
        resolved = true
      }
    },
  } as Hooks
}

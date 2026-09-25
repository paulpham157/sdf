// Print one herdr-e2b connection's material as a single JSON line on stdout.
//
// Usage: node plugin_bridge_helper.mjs <plugin>/src/connections.js <connection-id>
//
// The plugin's own code reads and validates the connection, so it stays the
// single owner of the connection format (ADR-0007).  Nothing is written to
// disk.  Failure lines carry a message that names the connection id, never
// material; the plugin's own error text is dropped because SDF cannot vouch
// for what it contains.
import { pathToFileURL } from "node:url"

function reply(payload, code) {
  process.stdout.write(`${JSON.stringify(payload)}\n`)
  process.exitCode = code
}

const [modulePath, id] = process.argv.slice(2)
if (!modulePath || !id) {
  reply({ ok: false, error: "usage", message: "expected <connections.js> <connection-id>" }, 2)
} else {
  try {
    const plugin = await import(pathToFileURL(modulePath).href)
    const record = plugin.readConnections().find((candidate) => candidate.id === id)
    if (!record) {
      reply({ ok: false, error: "unknown-connection", message: `unknown connection '${id}'` }, 3)
    } else {
      const material = plugin.connectionMaterial(record)
      reply({ ok: true, harness: record.harness, variables: material?.env ?? {} }, 0)
    }
  } catch {
    reply({ ok: false, error: "plugin-error", message: `the plugin could not provide connection '${id}'` }, 4)
  }
}

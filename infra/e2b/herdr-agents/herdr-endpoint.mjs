import crypto from "node:crypto"
import http from "node:http"
import { spawn } from "node:child_process"
import { readFileSync } from "node:fs"

const configuredToken = process.env.HERDR_ENDPOINT_TOKEN || ""
const tokenFile = process.env.HERDR_ENDPOINT_TOKEN_FILE || "/run/secrets/herdr_endpoint_token"
const port = Number(process.env.HERDR_ENDPOINT_PORT || "8787")
const maxBodyBytes = 64 * 1024
const maxOutputBytes = 1024 * 1024

if (!Number.isInteger(port) || port < 1 || port > 65535) {
  console.error("HERDR_ENDPOINT_PORT must be a valid TCP port")
  process.exit(2)
}

function authorized(request) {
  const token = readToken()
  if (!token) return false
  const value = request.headers.authorization || ""
  const received = Buffer.from(value.startsWith("Bearer ") ? value.slice(7) : "")
  const expected = Buffer.from(token)
  return received.length === expected.length && crypto.timingSafeEqual(received, expected)
}

function readToken() {
  if (configuredToken) return configuredToken
  try {
    return readFileSync(tokenFile, "utf8").trim()
  } catch {
    return ""
  }
}

function reply(response, status, payload) {
  const body = JSON.stringify(payload)
  response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" })
  response.end(body)
}

function readBody(request) {
  return new Promise((resolve, reject) => {
    const chunks = []
    let size = 0
    request.on("data", (chunk) => {
      size += chunk.length
      if (size > maxBodyBytes) {
        reject(new Error("request body too large"))
        request.destroy()
        return
      }
      chunks.push(chunk)
    })
    request.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")))
    request.on("error", reject)
  })
}

function runHerdr(command, timeoutMs) {
  return new Promise((resolve) => {
    const child = spawn("herdr", command.slice(1), {
      cwd: process.env.HERDR_ENDPOINT_CWD || process.cwd(),
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    })
    const stdout = []
    const stderr = []
    let stdoutSize = 0
    let stderrSize = 0
    let timedOut = false
    const collect = (target, size, chunk) => {
      if (size >= maxOutputBytes) return size
      const remaining = maxOutputBytes - size
      target.push(chunk.subarray(0, remaining))
      return size + Math.min(chunk.length, remaining)
    }
    child.stdout.on("data", (chunk) => { stdoutSize = collect(stdout, stdoutSize, chunk) })
    child.stderr.on("data", (chunk) => { stderrSize = collect(stderr, stderrSize, chunk) })
    const timer = setTimeout(() => {
      timedOut = true
      child.kill("SIGTERM")
    }, timeoutMs)
    const forceTimer = setTimeout(() => child.kill("SIGKILL"), timeoutMs + 1000)
    child.on("close", (code, signal) => {
      clearTimeout(timer)
      clearTimeout(forceTimer)
      resolve({
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
        exitCode: typeof code === "number" ? code : 128,
        signal,
        timedOut,
      })
    })
    child.on("error", (error) => {
      clearTimeout(timer)
      clearTimeout(forceTimer)
      resolve({ stdout: "", stderr: String(error), exitCode: 127, signal: null, timedOut })
    })
  })
}

const server = http.createServer(async (request, response) => {
  if (request.method !== "POST" || request.url !== "/v1/command") {
    reply(response, 404, { error: "not_found" })
    return
  }
  if (!authorized(request)) {
    const token = readToken()
    reply(response, token ? 401 : 503, { error: token ? "unauthorized" : "endpoint_not_configured" })
    return
  }
  try {
    const payload = JSON.parse(await readBody(request))
    const command = payload?.command
    const timeoutMs = Number(payload?.timeout_ms)
    if (!Array.isArray(command) || command.length < 1 || command[0] !== "herdr" || !command.every((part) => typeof part === "string")) {
      reply(response, 400, { error: "command must start with herdr and contain strings only" })
      return
    }
    if (!Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 120000) {
      reply(response, 400, { error: "timeout_ms must be an integer between 1 and 120000" })
      return
    }
    reply(response, 200, await runHerdr(command, timeoutMs))
  } catch (error) {
    reply(response, 400, { error: String(error) })
  }
})

server.listen(port, "0.0.0.0", () => {
  console.log(`Herdr endpoint listening on ${port}`)
})

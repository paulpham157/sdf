// Executes GHSA-g3hv-75wg-m249 against two algorithms:
// the published minimatch 10.2.6 (uncapped) and every firstPhasePreProcess
// copy inside the installed pi-lens package (entry and MCP chunks).
// A push cap aborts a preprocessor that grows without bound.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const PATTERN = "**/../a/b/**/../a/b/**/../a/b";
const OUTER_CAP = 32;
const SEGMENTS = ["**", "..", "a", "b"];

function arg(name) {
  const index = process.argv.indexOf(name);
  if (index < 0 || !process.argv[index + 1]) throw new Error(`missing ${name}`);
  return process.argv[index + 1];
}

const lensRoot = arg("--lens");
const minimatchRoot = arg("--minimatch");

function skipString(source, index) {
  const quote = source[index];
  let i = index + 1;
  while (i < source.length) {
    if (source[i] === "\\") {
      i += 2;
      continue;
    }
    if (source[i] === quote) return i + 1;
    i += 1;
  }
  throw new Error("unterminated string");
}

function extractArgs(source, openParen) {
  let i = openParen + 1;
  const found = [];
  let start = i;
  let depth = 1;
  while (i < source.length && depth > 0) {
    const ch = source[i];
    if (ch === "'" || ch === '"' || ch === "`") {
      i = skipString(source, i);
      continue;
    }
    if (ch === "/" && source[i + 1] === "/") {
      const nl = source.indexOf("\n", i);
      i = nl < 0 ? source.length : nl + 1;
      continue;
    }
    if (ch === "/" && source[i + 1] === "*") {
      const end = source.indexOf("*/", i + 2);
      i = end < 0 ? source.length : end + 2;
      continue;
    }
    if (ch === "(" || ch === "[" || ch === "{") depth += 1;
    else if (ch === ")" || ch === "]" || ch === "}") {
      depth -= 1;
      if (depth === 0) {
        found.push(source.slice(start, i).trim());
        break;
      }
    } else if (ch === "," && depth === 1) {
      found.push(source.slice(start, i).trim());
      start = i + 1;
    }
    i += 1;
  }
  return found.map((item) => item.trim()).filter(Boolean);
}

function objectKeys(literal) {
  const compact = literal.replace(/\s+/g, " ").trim();
  if (!compact.startsWith("{") || !compact.endsWith("}")) {
    throw new Error(`not an object literal: ${compact.slice(0, 80)}`);
  }
  if (compact.includes("...")) return { spread: true, keys: [] };
  const keys = [];
  let i = 1;
  let depth = 1;
  while (i < compact.length - 1 && depth > 0) {
    const ch = compact[i];
    if (ch === "'" || ch === '"' || ch === "`") {
      i = skipString(compact, i);
      continue;
    }
    if (ch === "{" || ch === "(" || ch === "[") depth += 1;
    else if (ch === "}" || ch === ")" || ch === "]") depth -= 1;
    else if (depth === 1 && /[A-Za-z_$]/.test(ch)) {
      const match = /^[A-Za-z_$][\w$]*/.exec(compact.slice(i));
      const key = match[0];
      let j = i + key.length;
      while (/\s/.test(compact[j])) j += 1;
      if (compact[j] === ":") keys.push(key);
      i = j;
      continue;
    }
    i += 1;
  }
  return { spread: false, keys };
}

function resolveOptions(source, callIndex, raw) {
  if (raw.startsWith("{")) return raw;
  const name = raw.trim();
  if (!/^[A-Za-z_$][\w$]*$/.test(name)) {
    throw new Error(`unresolved minimatch options: ${name.slice(0, 80)}`);
  }
  const marker = new RegExp(`(?:const\\s+)?(?<![\\w$])${name}\\s*=\\s*\\{`, "g");
  let brace = -1;
  let distance = Infinity;
  for (const match of source.matchAll(marker)) {
    const candidate = match.index + match[0].length - 1;
    const gap = Math.abs(candidate - callIndex);
    if (gap < distance) {
      distance = gap;
      brace = candidate;
    }
  }
  if (brace < 0) throw new Error(`no object literal for options identifier ${name}`);
  let depth = 0;
  for (let i = brace; i < source.length; i += 1) {
    const ch = source[i];
    if (ch === "'" || ch === '"' || ch === "`") {
      i = skipString(source, i) - 1;
      continue;
    }
    if (ch === "{") depth += 1;
    else if (ch === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(brace, i + 1);
    }
  }
  throw new Error(`unclosed options literal for ${name}`);
}

function callsIn(source, needle) {
  const found = [];
  const re = new RegExp(needle, "g");
  let match;
  while ((match = re.exec(source))) {
    const open = source.indexOf("(", match.index);
    found.push({ index: match.index, args: extractArgs(source, open) });
  }
  return found;
}

function methodText(source, signature, nextSignature) {
  const start = source.indexOf(signature);
  if (start < 0) return null;
  const end = source.indexOf(nextSignature, start + signature.length);
  if (end < 0) return null;
  const slice = source.slice(start, end);
  const close = slice.lastIndexOf("}");
  if (close < 0) return null;
  return slice.slice(0, close + 1).trim();
}

function loadPackagedPhase(source) {
  const phase = methodText(source, "firstPhasePreProcess(globParts) {", "secondPhasePreProcess(");
  const root = methodText(source, "#rootIndex(row) {", "hasMagic(");
  if (!phase || !root) throw new Error("packaged minimatch phase is incomplete");
  const Factory = new Function(`return class PackagedPhase {\n${root}\n${phase}\n};`);
  return { phase, cls: Factory() };
}

function runPackaged(source, braceExpandMax) {
  const { phase, cls } = loadPackagedPhase(source);
  const globParts = [[...SEGMENTS, ...SEGMENTS, ...SEGMENTS]];
  let outerCapHit = false;
  const push = globParts.push.bind(globParts);
  globParts.push = (...items) => {
    if (globParts.length + items.length > OUTER_CAP) {
      outerCapHit = true;
      throw new Error(`outer cap ${OUTER_CAP}`);
    }
    return push(...items);
  };
  const self = Object.assign(new cls(), {
    preserveMultipleSlashes: false,
    isWindows: false,
    options: braceExpandMax === undefined ? {} : { braceExpandMax },
  });
  const started = process.hrtime.bigint();
  let length = null;
  let error = null;
  try {
    length = self.firstPhasePreProcess(globParts).length;
  } catch (err) {
    error = err.message;
  }
  const ms = Number(process.hrtime.bigint() - started) / 1e6;
  return {
    hasCap: phase.includes("globParts.length >= braceExpandMax") && phase.includes("braceExpandMax = 1e5"),
    length,
    outerCapHit,
    error,
    ms: Math.round(ms * 1000) / 1000,
  };
}

function walkJs(dir, out = []) {
  for (const name of fs.readdirSync(dir)) {
    const full = path.join(dir, name);
    if (fs.statSync(full).isDirectory()) walkJs(full, out);
    else if (name.endsWith(".js") || name.endsWith(".mjs")) out.push(full);
  }
  return out;
}

const copies = [];
for (const file of walkJs(lensRoot)) {
  const source = fs.readFileSync(file, "utf8");
  if (!source.includes("firstPhasePreProcess(globParts) {")) continue;
  const capped = runPackaged(source, 4);
  const unbound = runPackaged(source, undefined);
  copies.push({
    file: path.relative(lensRoot, file),
    hasCap: capped.hasCap,
    cappedLength: capped.length,
    defaultLength: unbound.length,
    outerCapHit: capped.outerCapHit || unbound.outerCapHit,
    error: capped.error || unbound.error,
    cappedMs: capped.ms,
    defaultMs: unbound.ms,
  });
}

const require = createRequire(path.join(minimatchRoot, "package.json"));
const { Minimatch } = require(minimatchRoot);

function runPublished(options) {
  let entered = 0;
  let maxParts = 0;
  let outerCapHit = false;
  const original = Minimatch.prototype.firstPhasePreProcess;
  Minimatch.prototype.firstPhasePreProcess = function probe(globParts) {
    entered += 1;
    const push = globParts.push.bind(globParts);
    globParts.push = (...items) => {
      if (globParts.length + items.length > OUTER_CAP) {
        outerCapHit = true;
        throw new Error(`outer cap ${OUTER_CAP}`);
      }
      return push(...items);
    };
    try {
      const out = original.call(this, globParts);
      maxParts = Math.max(maxParts, out.length);
      return out;
    } finally {
      globParts.push = push;
    }
  };
  const started = process.hrtime.bigint();
  let setLen = null;
  let error = null;
  try {
    setLen = new Minimatch(PATTERN, options).set.length;
  } catch (err) {
    error = err.message;
  } finally {
    Minimatch.prototype.firstPhasePreProcess = original;
  }
  const ms = Number(process.hrtime.bigint() - started) / 1e6;
  return { entered, maxParts, setLen, outerCapHit, error, ms: Math.round(ms * 1000) / 1000 };
}

const entry = fs.readFileSync(path.join(lensRoot, "dist/index.js"), "utf8");
const libraryEnd = entry.indexOf("// dist/clients/path-utils.js");
if (libraryEnd < 0) throw new Error("pi-lens entry has no path-utils boundary");
const application = entry.slice(libraryEnd);
const entryCalls = [
  ...callsIn(application, "(?<![\\w$.])minimatch\\s*\\(").map((call) => ({ ...call, kind: "minimatch" })),
  ...callsIn(application, "new\\s+Minimatch\\s*\\(").map((call) => ({ ...call, kind: "Minimatch" })),
].map((call) => {
  const raw = call.args[2] ?? call.args[1];
  if (!raw) throw new Error(`${call.kind} call is missing an options argument`);
  const parsed = objectKeys(resolveOptions(application, call.index, raw));
  return { kind: call.kind, keys: parsed.keys, spread: parsed.spread };
});

const optimizationLevelMentions = [];
for (const match of entry.matchAll(/optimizationLevel/g)) {
  const around = entry.slice(Math.max(0, match.index - 16), match.index + 28);
  const wider = entry.slice(Math.max(0, match.index - 40), match.index + 40);
  const kind = around.includes("{ optimizationLevel = 1 }") || wider.includes("optimizationLevel ?? 1")
    ? "default"
    : around.includes("if (optimizationLevel >= 2)")
      ? "gate2"
      : around.includes("(optimizationLevel >= 1)")
        ? "gate1"
        : null;
  optimizationLevelMentions.push(kind ?? around.replace(/\s+/g, " "));
}
const firstPhaseAt = entry.indexOf("this.firstPhasePreProcess(");
const firstPhaseGated = /if \(optimizationLevel >= 2\) \{\s*globParts = $/.test(
  entry.slice(Math.max(0, firstPhaseAt - 120), firstPhaseAt),
);

const analyze = fs.readFileSync(path.join(lensRoot, "scripts/analyze-pi-lens-logs.mjs"), "utf8");
const analyzeCalls = callsIn(analyze, "(?<![\\w$.])minimatch\\s*\\(").map((call) => (
  objectKeys(call.args[2])
));

const concreteOptions = [
  { dot: true },
  { dot: true, nocase: true },
  { dot: true, nocase: false },
];

process.stdout.write(`${JSON.stringify({
  pattern: PATTERN,
  outerCap: OUTER_CAP,
  copies,
  vulnerable: {
    level2IgnoringCap: runPublished({ optimizationLevel: 2, dot: true, braceExpandMax: 4 }),
    level2: runPublished({ optimizationLevel: 2, dot: true }),
    callSiteOptions: concreteOptions.map((options) => ({ options, ...runPublished(options) })),
  },
  entryCalls,
  optimizationLevelMentions,
  firstPhaseGated,
  firstPhaseCalls: entry.split("this.firstPhasePreProcess(").length - 1,
  entryImportsChunk: /from\s+["']\.\/chunk-/.test(entry),
  entryMentionsMcpBin: entry.includes("dist/mcp/"),
  analyzeScript: {
    importsPackage: /from\s+["']minimatch["']/.test(analyze),
    calls: analyzeCalls,
  },
})}\n`);

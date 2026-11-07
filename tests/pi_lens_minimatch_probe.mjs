// Bounded GHSA-g3hv-75wg-m249 probe for the pi-lens tarball SDF loads.
// Prints one JSON object. Does not walk the filesystem with a hostile glob
// and refuses a preprocessor that grows past PART_CAP.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const PART_CAP = 16;
const PATTERN = "**/../a/b/**/../a/b/**/../a/b";
const ALLOWED_OPTION_KEYS = new Set(["dot", "nocase"]);

const args = process.argv.slice(2);
function arg(name) {
  const i = args.indexOf(name);
  if (i < 0 || !args[i + 1]) throw new Error(`missing ${name}`);
  return args[i + 1];
}

const lensRoot = arg("--lens");
const minimatchRoot = arg("--minimatch");
const entry = fs.readFileSync(path.join(lensRoot, "dist/index.js"), "utf8");
const published = fs.readFileSync(path.join(minimatchRoot, "dist/esm/index.js"), "utf8");

function extractBetween(source, startSig, endSig) {
  const idx = source.indexOf(startSig);
  if (idx < 0) return null;
  const end = source.indexOf(endSig, idx + startSig.length);
  if (end < 0) return null;
  return source.slice(idx, end);
}

function normalize(source) {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .replace(/(^|[^:\\])\/\/.*$/gm, "$1")
    .replace(/["']/g, "'")
    .replace(/\s+/g, " ")
    .trim();
}

function skipString(source, i) {
  const quote = source[i];
  i += 1;
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
  if (!literal.startsWith("{") || !literal.endsWith("}")) {
    throw new Error(`not an object literal: ${literal.slice(0, 80)}`);
  }
  if (literal.includes("...")) return { spread: true, keys: [] };
  const keys = [];
  let i = 1;
  let depth = 1;
  while (i < literal.length - 1 && depth > 0) {
    const ch = literal[i];
    if (ch === "'" || ch === '"' || ch === "`") {
      i = skipString(literal, i);
      continue;
    }
    if (ch === "{" || ch === "(" || ch === "[") depth += 1;
    else if (ch === "}" || ch === ")" || ch === "]") depth -= 1;
    else if (depth === 1 && /[A-Za-z_$]/.test(ch)) {
      const match = /^[A-Za-z_$][\w$]*/.exec(literal.slice(i));
      const key = match[0];
      let j = i + key.length;
      while (/\s/.test(literal[j])) j += 1;
      if (literal[j] === ":") keys.push(key);
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
  const marker = `const ${name} = {`;
  const at = source.lastIndexOf(marker, callIndex);
  if (at < 0 || callIndex - at > 2500) {
    throw new Error(`no nearby literal for options identifier ${name}`);
  }
  const brace = at + marker.length - 1;
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

const libraryEnd = entry.indexOf("// dist/clients/path-utils.js");
if (libraryEnd < 0) throw new Error("pi-lens entry has no path-utils boundary");
const application = entry.slice(libraryEnd);
const calls = [
  ...callsIn(application, "(?<![\\w$.])minimatch\\s*\\(").map((call) => ({ ...call, kind: "minimatch" })),
  ...callsIn(application, "new\\s+Minimatch\\s*\\(").map((call) => ({ ...call, kind: "Minimatch" })),
];
const entryCalls = calls.map((call) => {
  const raw = call.args[2] ?? call.args[1];
  if (!raw) throw new Error(`${call.kind} call is missing options and pattern`);
  const literal = resolveOptions(application, call.index, raw);
  const parsed = objectKeys(literal.replace(/\s+/g, " ").trim());
  return { kind: call.kind, keys: parsed.keys, spread: parsed.spread };
});

const levelSites = [...entry.matchAll(/const \{ optimizationLevel = 1 \} = this\.options;/g)].length;
const levelMentions = [];
for (const match of entry.matchAll(/optimizationLevel/g)) {
  const around = entry.slice(Math.max(0, match.index - 16), match.index + 28);
  const kind = around.includes("{ optimizationLevel = 1 }")
    ? "default"
    : around.includes("if (optimizationLevel >= 2)")
      ? "gate2"
      : around.includes("(optimizationLevel >= 1)")
        ? "gate1"
        : null;
  levelMentions.push(kind ?? around.replace(/\s+/g, " "));
}
const firstPhaseAt = entry.indexOf("this.firstPhasePreProcess(");
const firstPhaseGated = /if \(optimizationLevel >= 2\) \{\s*globParts = $/.test(
  entry.slice(Math.max(0, firstPhaseAt - 120), firstPhaseAt),
);
const bundledPhase = extractBetween(entry, "firstPhasePreProcess(globParts) {", "secondPhasePreProcess(");
const publishedPhase = extractBetween(published, "firstPhasePreProcess(globParts) {", "secondPhasePreProcess(");
if (!bundledPhase || !publishedPhase) throw new Error("missing firstPhasePreProcess");

const require = createRequire(path.join(minimatchRoot, "package.json"));
const { Minimatch } = require(minimatchRoot);

function run(options) {
  let entered = 0;
  let maxParts = 0;
  let capped = false;
  const original = Minimatch.prototype.firstPhasePreProcess;
  Minimatch.prototype.firstPhasePreProcess = function probe(globParts) {
    entered += 1;
    const push = globParts.push.bind(globParts);
    globParts.push = (...items) => {
      if (globParts.length + items.length > PART_CAP) {
        capped = true;
        throw new Error(`part cap ${PART_CAP}`);
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
  return { entered, maxParts, setLen, capped, error, ms: Math.round(ms * 1000) / 1000 };
}

const concreteOptions = [
  { dot: true },
  { dot: true, nocase: true },
  { dot: true, nocase: false },
];
const analyzePath = path.join(lensRoot, "scripts/analyze-pi-lens-logs.mjs");
const analyze = fs.readFileSync(analyzePath, "utf8");
const analyzeImportsPackage = /from\s+["']minimatch["']/.test(analyze);
const analyzeCalls = callsIn(analyze, "(?<![\\w$.])minimatch\\s*\\(").map((call) => {
  const literal = call.args[2];
  return objectKeys(literal.replace(/\s+/g, " ").trim());
});

const report = {
  partCap: PART_CAP,
  pattern: PATTERN,
  allowedOptionKeys: [...ALLOWED_OPTION_KEYS],
  entryImportsChunk: /from\s+["']\.\/chunk-/.test(entry),
  entryMentionsMcpBin: entry.includes("dist/mcp/"),
  optimizationLevelSites: levelSites,
  optimizationLevelMentions: levelMentions,
  firstPhaseGated: firstPhaseGated,
  firstPhaseCalls: entry.split("this.firstPhasePreProcess(").length - 1,
  firstPhaseEqual: normalize(bundledPhase) === normalize(publishedPhase),
  bundledHasCap: bundledPhase.includes("braceExpandMax") || /globParts\.length\s*>=/.test(bundledPhase),
  entryCalls,
  behavior: {
    level2: run({ optimizationLevel: 2, dot: true }),
    callSiteOptions: concreteOptions.map((options) => ({ options, ...run(options) })),
  },
  analyzeScript: {
    importsPackage: analyzeImportsPackage,
    calls: analyzeCalls,
  },
};
process.stdout.write(`${JSON.stringify(report)}\n`);

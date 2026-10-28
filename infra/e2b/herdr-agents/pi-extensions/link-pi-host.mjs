// ADR 0017: point the @earendil-works/* names the extensions import at the
// installed @mariozechner Pi packages (same files, so the same module
// instances). Usage: node link-pi-host.mjs <pi-coding-agent dir> <ext dir>
import fs from "node:fs";
import path from "node:path";

const [piDir, extDir] = process.argv.slice(2).map((p) => path.resolve(p));
const pkg = JSON.parse(fs.readFileSync(path.join(piDir, "package.json"), "utf8"));
if (pkg.name !== "@mariozechner/pi-coding-agent") throw new Error(`not Pi: ${pkg.name}`);
const find = (name) => {
  for (const base of [path.join(piDir, "node_modules", "@mariozechner"), path.dirname(piDir)]) {
    const dir = path.join(base, name);
    if (fs.existsSync(path.join(dir, "package.json"))) return dir;
  }
  throw new Error(`@mariozechner/${name} not found next to ${piDir}`);
};
const scope = path.join(extDir, "node_modules", "@earendil-works");
fs.mkdirSync(scope, { recursive: true });
for (const name of ["pi-coding-agent", "pi-tui", "pi-ai", "pi-agent-core"]) {
  const target = name === "pi-coding-agent" ? piDir : find(name);
  const link = path.join(scope, name);
  // Only ever replace our own links: a real directory here would be a second,
  // unpinned Pi pulled in as a dependency, which the template must not ship.
  const stat = fs.lstatSync(link, { throwIfNoEntry: false });
  if (stat && !stat.isSymbolicLink()) throw new Error(`refusing: ${link} is a real install, not a link`);
  if (stat) fs.unlinkSync(link);
  fs.symlinkSync(target, link, "dir");
  console.log(`@earendil-works/${name} -> ${target}`);
}

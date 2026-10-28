// ADR 0017: fail the image build unless every dependency is installed at the
// exact version pinned in package.json and package-lock.json, with an entry
// point present, and the @earendil-works names are links to the host Pi.
// Usage: node verify-pins.mjs <ext dir>
import fs from "node:fs";
import path from "node:path";

const dir = path.resolve(process.argv[2]);
const read = (p) => JSON.parse(fs.readFileSync(p, "utf8"));
const manifest = read(path.join(dir, "package.json"));
const lock = read(path.join(dir, "package-lock.json"));
for (const [name, pin] of Object.entries(manifest.dependencies)) {
  if (!/^\d+\.\d+\.\d+$/.test(pin)) throw new Error(`${name}: ${pin} is not an exact version`);
  const locked = lock.packages[`node_modules/${name}`];
  if (!locked || locked.version !== pin || !locked.integrity?.startsWith("sha512-")) {
    throw new Error(`${name}: lockfile does not pin ${pin} with a sha512 integrity`);
  }
  const installed = read(path.join(dir, "node_modules", name, "package.json"));
  if (installed.name !== name || installed.version !== pin) {
    throw new Error(`${name}: installed ${installed.version}, pinned ${pin}`);
  }
  console.log(`${name}@${pin} ${locked.integrity}`);
}
for (const name of ["pi-coding-agent", "pi-tui", "pi-ai", "pi-agent-core"]) {
  const link = path.join(dir, "node_modules", "@earendil-works", name);
  if (!fs.lstatSync(link).isSymbolicLink()) throw new Error(`${link} is not a link to the host Pi`);
  if (!fs.existsSync(path.join(link, "package.json"))) throw new Error(`${link} is dangling`);
}

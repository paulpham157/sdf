// ADR 0017: fail the image build unless every dependency is installed at the
// exact version pinned in package.json and package-lock.json, with an entry
// point present, and no Pi package of its own (the host Pi provides it).
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
// No second Pi: the host Pi supplies these modules to the extensions.
for (const scope of ["@earendil-works", "@mariozechner"]) {
  const own = path.join(dir, "node_modules", scope);
  if (fs.existsSync(own)) throw new Error(`${own} exists: the extensions must not ship their own Pi`);
}

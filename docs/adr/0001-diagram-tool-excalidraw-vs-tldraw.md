# Diagram tool: keep tldraw, or move the docs diagrams to Excalidraw

- Status: Proposed
- Date: 2026-11-26

## Context

sdf draws its documentation figures in tldraw and checks in a PNG of
each figure. The question is whether those figures should move to
[Excalidraw](https://github.com/excalidraw/excalidraw). This record
recommends keeping tldraw. It does not convert a diagram, and it does
not change Go code. Sources were read on 2026-11-26. A claim this pass
did not re-check is marked unverified in [Claims left unverified](#claims-left-unverified).

### What sdf stores today

`docs/diagrams/` holds sixteen `.tldr` sources and sixteen PNG renders,
one pair per figure. A search of the tree found no other `.tldr` file.
The only other PNG under `docs/` is `docs/images/decisions-ui.png`, a
screenshot of the Decisions page, separate from these drawings.

Every `.tldr` is one JSON line. The envelope is
`tldrawFileFormatVersion` 1 and schema version 2, which matches the
current reader in tldraw's [`file.ts`](https://github.com/tldraw/tldraw/blob/main/packages/tldraw/src/lib/utils/tldr/file.ts).
Each file also stores editor records beside the shapes: one `pointer`,
`camera`, `instance_page_state`, `user`, `page`, `document`, and
`instance`. Across the sixteen files the shapes are 138 rectangles, 68
arrows, and 46 text shapes (252 shapes). Every shape color in the JSON
is the named color `black`. Dashed strokes appear on 22 rectangles.
There is no freehand, image, frame, or embed. Labels are Latin text
plus three punctuation characters (an em dash, an en dash, and an
ellipsis). There is no CJK text and no emoji.

Every PNG is 1152 pixels wide. Heights run from 1616
(`12-claude-code-spans.png`) to 4556
(`03-decision-relations.png`). [dev-docs/diagrams.md](https://github.com/paulpham157/sdf/blob/main/dev-docs/diagrams.md)
says the PNG is a 2× render meant to sit at about 576 CSS pixels, on
cream `#F6F1E8`, with charcoal `#2F3640`, rust `#C65A2E` on one
highlighted step, and secondary ink `#4E5864`. The same page says
tldraw.com's own export uses its default blue, purple, green, and
orange, so a default export is not checked in unchanged. The cream,
charcoal, and rust values live in the PNG, not in the `.tldr`.

Nothing in `.github/workflows/` or the Makefile renders a `.tldr`.
`dev-docs/diagrams.md` says the directory has no render toolchain and
that the `.tldr` and `.png` names stay in lockstep. The authoring steps
are: open the file on [tldraw.com](https://www.tldraw.com) (File → Open,
File → Save) or in the [tldraw VS Code extension](https://marketplace.visualstudio.com/items?itemName=tldraw-org.tldraw-vscode),
then export a PNG by hand at 2× on the palette above.

| Pair | Shapes | PNG height | Where the PNG is embedded |
| --- | --- | --- | --- |
| `01-architecture` | 17 | 3532 | `README.md`, `docs/index.md` |
| `02-receiver` | 22 | 3748 | `docs/receiver.md` |
| `03-decision-relations` | 34 | 4556 | `docs/outcomes.md` |
| `04-decision-commands` | 16 | 3972 | `docs/outcomes.md` |
| `05-decision-graph` | 26 | 4064 | `docs/outcomes.md` |
| `06-verify-pipeline` | 16 | 4120 | `docs/verify.md` |
| `07-github-action-verify` | 16 | 3336 | `docs/action.md` |
| `08-ledger-chain` | 12 | 2584 | `docs/ledger.md` |
| `09-ci-workflows` | 7 | 2896 | `CONTRIBUTING.md` |
| `10-git-hooks` | 13 | 2944 | `docs/receiver.md` |
| `11-task-declare` | 11 | 2636 | `docs/tasks.md` |
| `12-claude-code-spans` | 10 | 1616 | `docs/agents.md` |
| `13-grok-spans` | 11 | 2000 | `docs/agents.md` |
| `14-qwen-spans` | 11 | 1884 | `docs/agents.md` |
| `15-summary-cost` | 16 | 3348 | `docs/summary.md` |
| `16-session-outcomes` | 14 | 2828 | `docs/outcomes.md` |

Each embed is followed by a source line that points at the `.tldr` and
at tldraw.com, and that names `.tldr` as the official extension.
`design/tokens.md` discusses `01-architecture.png` as the figure the
README and the docs home use. It does not embed the file.

The landing page repository
[`paulpham157/sdf-landingpage`](https://github.com/paulpham157/sdf-landingpage)
(tree at the time of this research, `e2230d2145475a23c912b8930273db362e2ae34e`)
contains no `.tldr`, no `docs/diagrams/` path, and no file whose name is
one of these PNGs. A code search of that repository for `tldr`,
`tldraw`, `excalidraw`, `diagrams/`, and `architecture.png` returned no
hits. The page does not need a change for this decision.

### The Decisions graph is a different picture

`docs/diagrams/05-decision-graph.png` is a static explanation of one
chapter's lanes. The graph a person sees in the UI is built by
`internal/decisiongraph`: `Build` reads stored decisions and summarized
sessions and returns a `View`. The package comment says the same inputs
give the same view, and that a node exists only for a stored decision,
an alternative named on one, or a session outcome a stored edge points
at. Replacing tldraw does not replace that view, and Excalidraw would
not replace it either.

### Docs site, dark mode, and the contrast check

The site is MkDocs Material (`mkdocs.yml`). `mkdocs build --strict` is
the Docs workflow build. In the slate scheme,
`docs/stylesheets/extra.css` paints `img[src*="diagrams/"]` and
`img.sdf-diagram` with `--sdf-color-diagram-plate` (cream `#F6F1E8`).
`tests/docs-a11y/contrast.spec.js` asserts that plate on the home page
and on `/outcomes/`, then runs axe `color-contrast`. Axe reads the page,
not the pixels inside a PNG. The alt text on each embed is the text a
non-visual reader gets.

The site copies `design/tokens.css` at build time and hosts its own
fonts (`mkdocs.yml` sets `font: false` so Material does not request
Google Fonts). A diagram that arrived as a client-rendered widget would
sit outside that plate rule, because the rule matches `img`.

## Decision drivers

1. The published figure is the PNG. GitHub Markdown and the docs site
   already show it. The source file is what a contributor edits.
2. The palette is part of the design tokens. A default export from
   either tool's stock colors fails that bar. tldraw.com's default
   palette is called out for this reason in `dev-docs/diagrams.md`.
3. sdf is Apache-2.0. A render tool that contributors and CI run should
   stay under a permissive licence. The tldraw SDK's licence allows
   development use and withholds production use unless a separate key
   is issued.
4. Today's render is manual, and the docs say so. A new tool is worth
   its cost when it makes that render reproducible, or when the current
   licence blocks the way the files are actually produced.
5. The sixteen figures were drawn to match the code. A lossy converter
   would put words in the wrong place. The words are the document.
6. One authoring recipe is enough. `dev-docs/diagrams.md` is that recipe.
7. The UI graph stays data-driven. This choice is about static docs
   figures only.

## Considered options

1. Keep tldraw. Edit `.tldr` on tldraw.com or in the VS Code extension.
   Keep exporting the PNG by hand. Leave the sixteen pairs where they are.
2. Adopt Excalidraw for new diagrams only. Leave the existing pairs as
   `.tldr` plus PNG. Draw the next figure as `.excalidraw` plus PNG.
3. Replace tldraw with Excalidraw. Redraw the sixteen figures, switch
   the authoring page, and point the source lines at Excalidraw.

Mermaid is already wired in `mkdocs.yml` (`pymdownx.superfences`) and
`design/tokens.md` describes how a Mermaid fence would take the token
colors. The architecture figure is a PNG on purpose. Mermaid is a
different notation, and it is outside this choice.

## tldraw

### Licence: editor and SDK

The [tldraw SDK licence](https://github.com/tldraw/tldraw/blob/main/LICENSE.md)
permits development, modification, and bundling, and it forbids
production use, interference with licence-key checks, and distributing
the SDK as a standalone product. "Production" in that text is a
deployment that serves end users. Development is internal hosting for
development, testing, or staging.

The [licence docs](https://tldraw.dev/community/license) say the default
terms allow development only. Production needs a trial key (100 days,
one per commercial unit, and the SDK phones home with a hash of the
key), a commercial key (no watermark), or a hobby key (the "made with
tldraw" watermark stays, and the grant is discretionary). The
[FAQ](https://tldraw.dev/faq) says the SDK is free in development and,
in production, needs one of those keys. It also says the SDK is source
available and is not open source. The same FAQ says tldraw.com is the
flagship demo, "a very good free whiteboard," and that early SDK builds
ship there for testing. Using the website to draw a figure is the demo.
Embedding `<Tldraw>` in sdf, or shipping a script that imports the SDK
for readers of the repo, is the SDK licence.

sdf does neither. The binary does not import tldraw. The docs do not
load it. Contributors open tldraw.com or the VS Code extension and
commit JSON and PNG.

The [VS Code extension listing](https://marketplace.visualstudio.com/items?itemName=tldraw-org.tldraw-vscode)
says the extension project is MIT and that the tldraw SDK inside it is
under the tldraw licence. The extension README tells people to commit
`.tldr` files next to code and to open the same files on tldraw.com.
Whether that extension shows a watermark or asks for a licence key was
not re-checked past that listing. See [Claims left unverified](#claims-left-unverified).

A headless exporter that imports the SDK would put the SDK on the
contributor path and in CI. The FAQ's answer to server-side images is
to drive a browser with Playwright or Puppeteer. There is no statement
that a CI job rendering documentation is "development" under the
licence. This record treats that reading as unresolved, and it treats
"do not import the SDK from this repo" as the safe line.

### File format

`TLDRAW_FILE_EXTENSION` is `.tldr`. The MIME type is
`application/vnd.tldraw+json`. `LATEST_TLDRAW_FILE_FORMAT_VERSION` is 1.
`parseTldrawJsonFile` rejects a higher file-format version with
`fileFormatVersionTooNew`. A file at version 1 can still hold old
records; the loader migrates them. Migration failure, including
`TargetVersionTooNew`, refuses the file. The comment on the constant
says a new file-format version has to keep a parser for the old one.
The envelope version has stayed at 1 while record sequences move. The
checked-in files carry sequences as high as `com.tldraw.instance` 26
and `com.tldraw.shape.note` 15. An older editor that lacks a migration
for a sequence in a future save will fail the file even though the
envelope version is still 1. That is forward migration, not a frozen
schema.

`serializeTldrawJson` writes `JSON.stringify` of the whole store,
including camera and instance state, with no indent. That matches the
one-line files in `docs/diagrams/`.

### Export, CLI, editor, offline

The editor exports SVG and PNG through the browser. SVG generation
mounts React on a DOM node (`exportToSvg.tsx`), and PNG goes through
canvas (`getSvgAsImage.ts`). That coupling is described in tldraw's
[issue 7539](https://github.com/tldraw/tldraw/issues/7539) (January
2026). The issue is closed by its author. The thread contains a
research comment and no merged CLI. A search of `tldraw/tldraw` for a
package named `@tldraw/cli` returned nothing. [Issue 1491](https://github.com/tldraw/tldraw/issues/1491)
(July 2024) says an official CLI was not planned then, and points at a
Puppeteer example.

[`@kitschpatrol/tldraw-cli`](https://github.com/kitschpatrol/tldraw-cli)
is a third-party exporter (SVG, PNG, and `.tldr`, with scale, dark
mode, frames, and pages). Its README says the tldraw site moves faster
than the tool, and that local `.tldr` export is the more stable path.
It drives the editor in a browser, so it pulls in the SDK. This record
does not adopt it.

The FAQ says the SDK's default store is offline, and that a browser can
generate images. tldraw.com itself is a hosted app. This pass did not
install it as a PWA or load it with the network off. The VS Code
extension opens a local `.tldr` without a separate account, which is
the offline edit path the listing documents.

### GitHub

GitHub renders a PNG in Markdown. It does not render a `.tldr` as a
picture. The Linguist [`languages.yml`](https://github.com/github-linguist/linguist/blob/main/lib/linguist/languages.yml)
fetched for this research has no `tldr` and no `excalidraw` entry, so
Linguist does not assign either extension a language. Whether the
GitHub blob view still highlights a `.tldr` as JSON was not checked on
a live file. A one-line file diffs as one replaced line, and the line
includes camera and selection state, so a small move of the viewport
rewrites the whole document in the pull request.

## Excalidraw

### Licence and format

[Excalidraw](https://github.com/excalidraw/excalidraw) is MIT
([`LICENSE`](https://github.com/excalidraw/excalidraw/blob/master/LICENSE),
copyright 2020 Excalidraw). The README calls the npm package free and
open source, lists export to PNG, SVG, and the clipboard, and calls
`.excalidraw` an open format: a JSON file.

The [JSON schema note](https://github.com/excalidraw/excalidraw/blob/master/dev-docs/docs/codebase/json-schema.mdx)
describes a plaintext document with `type` `"excalidraw"`, a numeric
`version`, `source`, `elements`, `appState`, and `files`. The example
uses `version` 2. `serializeAsJSON` in
[`json.ts`](https://github.com/excalidraw/excalidraw/blob/master/packages/excalidraw/data/json.ts)
writes that object with `JSON.stringify(data, null, 2)`, so a normal
save is pretty-printed. A 2021 maintainer reply
([discussion 3545](https://github.com/excalidraw/excalidraw/discussions/3545))
calls the schema one that other applications have to implement on
purpose. "Open format" here means the JSON is documented and
unencumbered, not that a generic diagram tool reads it.

Elements carry `version` and `versionNonce`, which change when an
element is edited. A pretty-printed diff is readable, and a nudge still
rewrites those fields. `roughness` has three steps in
[`constants.ts`](https://github.com/excalidraw/excalidraw/blob/master/packages/common/src/constants.ts):
architect `0`, artist `1`, cartoonist `2`. The default element is
artist. Architect (`0`) is the setting that matches sdf's straight
boxes. Stroke and background are hex colors, so `#2F3640`, `#C65A2E`,
`#4E5864`, and a canvas `viewBackgroundColor` of `#F6F1E8` can live in
the source. tldraw's checked-in sources cannot say that today: they say
`black`, and the palette is applied while exporting.

### Embedded scene, export, CLI

Export can embed the scene. In
[`data/index.ts`](https://github.com/excalidraw/excalidraw/blob/master/packages/excalidraw/data/index.ts),
a PNG with `exportEmbedScene` stores `serializeAsJSON` in a PNG text
chunk and uses the extension `.excalidraw.png`. An SVG uses
`.excalidraw.svg`. [PR 2219](https://github.com/excalidraw/excalidraw/pull/2219)
added that embed. [PR 8975](https://github.com/excalidraw/excalidraw/pull/8975)
(merged 2025-01-05) moved the SVG payload into `<metadata>`, closing
[issue 7338](https://github.com/excalidraw/excalidraw/issues/7338),
where the base64 sat in the SVG in a way some screen readers spoke.

The editor at excalidraw.com is a PWA and works offline, per the
project README. Opening `.excalidraw.png` and `.excalidraw.svg` from
the installed PWA's file handler is still an open request
([issue 10689](https://github.com/excalidraw/excalidraw/issues/10689)).
Drag-and-drop and File → Open already load them.

[`@excalidraw/cli`](https://github.com/excalidraw/cli) (MIT, repository
created 2026-10-08) renders a local `.excalidraw` to PNG or SVG with
the browser already installed. The [render docs](https://plus.excalidraw.com/docs/cli/rendering)
say `excalidraw render drawing.excalidraw --out drawing.png` needs no
account and no API key. `--scale` goes up to 16 (default 1). `--theme`
is `light` or `dark`. The CLI does not download a browser. SVG output
embeds the fonts the text uses, subset to the characters in the scene.
CJK in Excalifont downloads Xiaolai on first use. These sixteen
diagrams have no CJK, so that download would not run for them. Emoji
come from the system font. The CLI is young, and this pass did not run
it against a sample scene.

[`@excalidraw/utils`](https://github.com/excalidraw/excalidraw/blob/master/packages/utils/README.md)
exports `exportToSvg` (returns an `SVGElement`) and `exportToBlob`
(returns a `Blob`). The documented use is a browser or a bundler, and
the snippet reads `svg.outerHTML` and `window.URL`. It is not a
headless Node renderer. [Issue 6723](https://github.com/excalidraw/excalidraw/issues/6723)
reported misplaced arrow labels from utils 0.1.2. In May 2024 a
maintainer said the package had not yet shipped the export fixes from
`@excalidraw/excalidraw`. The npm page listed 0.1.8 at research time.
This pass did not render with 0.1.8, so whether that bug remains is
unverified. The CLI, which drives the real editor in a browser, is the
export path worth using if a later change automates PNG output.

[`rickardp/excalidraw-image`](https://github.com/rickardp/excalidraw-image)
is a third-party MIT binary that renders and can extract an embedded
scene. It is not the Excalidraw project. This record does not depend on
it.

### Editor, libraries, GitHub, accessibility

The [VS Code extension](https://github.com/excalidraw/excalidraw-vscode)
(MIT, also linked from the Excalidraw README) opens `.excalidraw`,
`.excalidraw.json`, `.excalidraw.svg`, and `.excalidraw.png`. It can
edit an image whose scene is embedded, and it can follow the VS Code
theme (`light`, `dark`, or `auto`). A workspace library path can point
at a shared `.excalidrawlib`.

Public shape libraries live at
[libraries.excalidraw.com](https://libraries.excalidraw.com), with the
catalog in
[`excalidraw/excalidraw-libraries`](https://github.com/excalidraw/excalidraw-libraries).
sdf's figures are rectangles, arrows, and text. A library would not
remove the need to place those labels.

GitHub Markdown shows PNG and SVG. It does not show a `.excalidraw`
JSON file as a picture. `.excalidraw.png` is a PNG, so it displays, and
the scene travels inside it, at the cost of a binary diff. SVG as an
`<img>` has a history of dropping Excalidraw's font: [issue 4855](https://github.com/excalidraw/excalidraw/issues/4855)
(closed 2024-07-30) and the VS Code extension
[issue 28](https://github.com/excalidraw/excalidraw-vscode/issues/28).
The CLI now documents subsetted embedded fonts. This pass did not embed
a fresh SVG in a GitHub README to see whether the `<img>` sandbox still
drops that font. The checked-in PNG is the path that already works.
PNG text is not page text. The alt attribute on the Markdown image is
what the docs expose, and that is already how these figures are
written. An inline SVG would put text in the DOM, where axe
`color-contrast` could see it, and it would miss the `img[src*="diagrams/"]`
plate. Dark mode in the Excalidraw editor and `--theme dark` on the CLI
are real. The docs site wants the light drawing on a cream plate in
both schemes (`design/tokens.md`). A dark-theme export would fight that
plate.

### MkDocs plugins

[`mkdocs-excalidraw`](https://github.com/qdeli187/mkdocs-excalidraw)
replaces an image whose `src` contains `.excalidraw` with a custom
element and injects `excalidraw-renderer.bundle.js` from the built
site (current `plugin.py` copies the bundle into `site/assets/`). The
page then draws the scene in the browser. The plate CSS would not
match, because the tag is no longer `img`. The contrast job would be
looking at a widget. The plugin is a small third-party project. sdf's
docs build is a static Material site with vendored fonts. A client-side
renderer is a poor fit for that build.

## Conversion

Neither tldraw nor Excalidraw ships a `.tldr` to `.excalidraw`
converter. This pass did not run a third-party converter, and this
change does not add one.

Three third-party efforts describe the mapping, and each one says it
approximates:

- [OmniCanvas](https://omnicanvasnotes.com/tools/tldraw-viewer) converts
  in the browser and lists clouds, elbow arrows, highlighter strokes,
  crops, and fonts as approximations.
- [Blending Links](https://blendinglinks.com/convert) reads `.tldr` and
  writes `.excalidraw`, and it says elbow and curved connectors come
  out straight and that pressure on freehand strokes is dropped.
- [`lattice-core`'s `convert_tldr`](https://github.com/leo1oel/lattice/pull/1084)
  (merged 2026-11-18) is a deterministic Rust converter. It maps geo
  shapes onto rectangle, ellipse, or diamond, flattens rich text, and
  sets roughness to 0. Colors go to Excalidraw's palette, not to an
  arbitrary hex. A bend becomes a three-point curve.

The sdf files are an easy subset for a geometric mapping: rectangles,
straight arrows, and text, one page, no images. They are a poor subset
for a pixel match. The checked-in PNG uses cream, charcoal, and rust.
The `.tldr` says `black` and stores tldraw's default color names. A
converter that maps `black` onto Excalidraw's black, or onto
Excalidraw's palette, will not reproduce `#F6F1E8` / `#2F3640` /
`#C65A2E`. Rich text in the `.tldr` is a ProseMirror document. Arrow
geometry is tldraw's start and end points, not Excalidraw's points
array. Even a careful converter needs a human to check every label
against the PNG that the docs already publish.

Redraw effort, as an estimate and not a timed trial: 252 shapes, all
of three kinds, labels already written in the PNG and in the
surrounding doc. Copying each figure (boxes, arrows, and the exact
label text) and exporting a 1152-wide PNG is about a day of careful
work for someone who has the existing PNGs side by side, plus a review
pass so a flag name does not move. The three span trees (10 to 11
shapes) are the short ones. `03-decision-relations` (34 shapes) and the
tall architecture and pipeline figures are the long ones. That is small
next to a product change, and it is large enough to risk the wording.
The wording was the point of drawing them.

## Pros and cons

### Keep tldraw

- The authoring page, the source lines, and the sixteen pairs already
  agree. Accepting this option changes no figure and no link.
- Contributors use tldraw.com, which the FAQ describes as a free
  whiteboard, or the VS Code extension. sdf does not import the SDK, so
  the production-key and watermark rules are not in force for the
  current workflow.
- The PNG, the alt text, the cream plate, and the axe check stay as
  they are.
- The `.tldr` is one line and carries camera and instance state, so
  pull-request diffs of the source are poor. Review of the figure is
  review of the PNG, which is the current practice.
- The palette is applied at export time. A contributor who checks in
  tldraw.com's default colors ships the wrong plate. The authoring page
  already warns about that.
- There is still no official headless exporter. Automating the PNG
  later means a browser plus the SDK, which reopens the licence
  question this option currently avoids.
- A future tldraw that bumps a record sequence can refuse to open a
  file saved by a newer editor. The envelope version staying at 1 does
  not remove that.

### Adopt Excalidraw for new diagrams only

- New figures could store the token hex colors and roughness 0 in the
  JSON, and a later CI job could run the MIT CLI.
- The existing sixteen PNGs stay valid. No wording has to be redrawn
  up front.
- `dev-docs/diagrams.md` would have two recipes, two extensions, and
  two places to look in a pull request. The source line under each
  figure would depend on which tool drew it.
- The licence pressure this option relieves is the pressure of a future
  SDK import. The current workflow does not have that import. The cost
  of two formats arrives immediately.

### Replace tldraw with Excalidraw

- One permissive tool, pretty-printed JSON, hex colors in the source,
  an official local CLI, and an embedded-scene PNG if a single file
  should stay editable.
- The VS Code extension edits `.excalidraw` offline in the same sense
  the tldraw extension edits `.tldr`. excalidraw.com is a PWA.
- Every existing figure has to be redrawn or accepted from an
  approximate converter. No official converter exists. A wrong label in
  a verify or ledger figure is a doc bug.
- The published PNG still has to be cream, charcoal, and rust, 1152
  wide, at 2×. Excalidraw's default look (roughness 1, white canvas,
  its own palette) is as wrong for this repo as tldraw.com's default
  export. The hand-export discipline moves. It does not disappear.
- `.excalidraw.png` displays on GitHub and round-trips, and it diffs as
  a binary. The reviewable text is the `.excalidraw` JSON, if that file
  is what the repo commits.
- `version` and `versionNonce` still churn in review.
- The MkDocs plugin would turn figures into a client-side widget and
  drop them out of the plate and contrast checks. It should not be added.
- The Decisions graph, the landing page, and the screenshot
  `decisions-ui.png` are unaffected. The landing page does not reference
  these files.

## Recommendation

Keep tldraw.

The SDK licence is a real constraint on embedding tldraw or on putting
the SDK in CI. It is not a constraint on the workflow sdf uses. That
workflow is the hosted whiteboard plus a hand-exported PNG, and the
PNG is what readers see. Excalidraw is the better tool on licence,
on JSON that can store `#F6F1E8` and `#2F3640`, and on having an
official MIT CLI. Those advantages pay off when someone automates the
render. They do not pay off a redraw of 252 shapes whose wording was
checked against the code, and they do not pay off a second authoring
recipe while the render stays manual.

Adopt Excalidraw for new diagrams only if a later record wants the CLI
badly enough to keep two formats. This record does not. One recipe,
the one in `dev-docs/diagrams.md`, stays.

Revisit this record when a change proposes a headless render in CI.
At that point prefer the Excalidraw CLI over any tool that imports the
tldraw SDK, and supersede this ADR. Until then, do not add
`@kitschpatrol/tldraw-cli`, `@excalidraw/utils`, or `mkdocs-excalidraw`.

## Consequences

If this recommendation is accepted:

- `docs/diagrams/*.tldr` and the matching PNGs stay. Source lines keep
  pointing at tldraw.com.
- New documentation figures follow `dev-docs/diagrams.md`: `.tldr`
  source, hand-exported 2× PNG, cream background, charcoal and rust.
- sdf still does not depend on the tldraw SDK. A pull request that
  adds that dependency is a new decision, not a consequence of this one.
- Diffs of `.tldr` files stay hard to read. Reviewers keep looking at
  the PNG.
- The docs contrast job keeps asserting the cream plate on
  `img[src*="diagrams/"]`. No new client-side script is added to the
  docs build.
- The Decisions graph in the UI is unchanged. The landing page is
  unchanged.

If this recommendation is rejected in favor of a full move, the
migration plan below is the work. Rejecting it in favor of "new
diagrams only" means writing a second recipe into `dev-docs/diagrams.md`
and living with both extensions. That rejection should say who redraws
an old figure when it next changes, or the old `.tldr` will be edited
forever.

## Migration plan if adopted

This plan is the work to do if a later decision accepts a full move to
Excalidraw. It is not work this record starts.

1. Leave every `.tldr` and PNG in place until its replacement PNG is
   reviewed next to the old one. Do not delete a source in the same
   commit that introduces an unreviewed redraw.
2. Redraw each figure in Excalidraw at roughness 0 (architect). Set the
   canvas background to `#F6F1E8`. Set text to `#2F3640`, the highlighted
   step to `#C65A2E`, and secondary ink to `#4E5864`. Copy the label
   text from the current PNG. Keep one flag per line, as the authoring
   page requires today. Do not treat a third-party `.tldr` converter as
   the source of the new file. A converter may be used as a sketch, and
   the labels still have to be checked. Effort: about a day of redraw
   for all sixteen, plus review. That figure is an estimate from the
   shape counts above. It was not timed.
3. Commit a pretty-printed `.excalidraw` next to a plain `.png`. Keep
   the PNG as the file Markdown embeds, so the README, the docs site,
   and the cream-plate CSS stay valid. A `.excalidraw.png` with an
   embedded scene is optional and, if used, is a second artifact. The
   JSON is what reviewers read. The PNG is what readers see.
4. Export the PNG at scale 2, about 1152 pixels wide, with the light
   theme. The official CLI, pinned, is the way to make that repeatable:
   `excalidraw render FILE.excalidraw --out FILE.png --scale 2 --theme light`,
   with a browser installed and no CJK font download for these labels.
   Until that command is in CI, export from the editor with the same
   settings and say so in `dev-docs/diagrams.md`. Do not use
   `@excalidraw/utils` as a Node script.
5. After the new PNG matches, update the source line under the figure,
   then remove the `.tldr`. Update `dev-docs/diagrams.md` when the first
   figure moves, not after all sixteen have moved.
6. Do not add `mkdocs-excalidraw`. Do not inline the SVG into the page.
   Do not point the landing page at these files. It does not use them.
7. Do not change `internal/decisiongraph` or the Decisions screenshot.

A CI render, if added later, belongs in the Docs workflow, fails when
the committed PNG differs, and uses a pinned `@excalidraw/cli`. The
contrast job already installs Chromium for Playwright. Sharing that
browser is a detail for the change that adds the render. It is not
required to store `.excalidraw` files.

## Claims left unverified

- The live tldraw.com canvas was not opened, logged out, to see whether
  the "made with tldraw" watermark is visible. [Issue 7698](https://github.com/tldraw/tldraw/issues/7698)
  proposed showing it for logged-out users. This record does not depend
  on that watermark, because sdf does not embed the site.
- The VS Code tldraw extension was not installed. The marketplace text
  says the extension is MIT and the SDK is under the tldraw licence. A
  watermark or a licence-key prompt inside the extension was not
  observed.
- A current Excalidraw SVG with subsetted fonts was not loaded through
  GitHub's Markdown image renderer. Older reports say the hand-drawn
  font drops when the SVG is an `<img>`. The CLI documents embedded
  fonts. Both can be true if the `<img>` sandbox ignores `@font-face`.
- `@excalidraw/utils` 0.1.8 was not used to render a scene. The arrow
  bug in issue 6723 was reported against 0.1.2.
- No third-party converter was run on these sixteen files.
- A GitHub blob page for a `.tldr` was not opened. Linguist's language
  list has no entry for the extension. Highlighting on the blob view
  can still be something else.
- Landing-page evidence is the git tree plus a code search. A search
  index can miss a string. The tree itself has no copy of these diagram
  files.

## Links

- MADR: <https://adr.github.io/madr/>
- sdf authoring: [dev-docs/diagrams.md](https://github.com/paulpham157/sdf/blob/main/dev-docs/diagrams.md)
- sdf design spec for decision categories, a different kind of document: [Decision categories](../design/decision-category.md)
- tldraw licence: <https://github.com/tldraw/tldraw/blob/main/LICENSE.md>
- tldraw licence docs: <https://tldraw.dev/community/license>
- tldraw FAQ (editor and SDK): <https://tldraw.dev/faq>
- tldraw file reader: <https://github.com/tldraw/tldraw/blob/main/packages/tldraw/src/lib/utils/tldr/file.ts>
- tldraw CLI request: <https://github.com/tldraw/tldraw/issues/7539>
- tldraw VS Code extension: <https://marketplace.visualstudio.com/items?itemName=tldraw-org.tldraw-vscode>
- Excalidraw repository and MIT licence: <https://github.com/excalidraw/excalidraw>
- Excalidraw JSON schema: <https://github.com/excalidraw/excalidraw/blob/master/dev-docs/docs/codebase/json-schema.mdx>
- Excalidraw CLI render docs: <https://plus.excalidraw.com/docs/cli/rendering>
- Excalidraw VS Code extension: <https://github.com/excalidraw/excalidraw-vscode>
- Excalidraw libraries: <https://libraries.excalidraw.com>
- SVG scene payload and screen readers: <https://github.com/excalidraw/excalidraw/pull/8975>
- Landing page repository: <https://github.com/paulpham157/sdf-landingpage>

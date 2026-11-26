# Architecture decision records

ADRs in this directory record a choice about how the sdf repository is
built or documented. Each record is a Markdown file in the
[MADR](https://adr.github.io/madr/) shape: context, the options that were
compared, a recommendation, and what follows if that recommendation is
accepted. The status on a new record is Proposed until a maintainer
accepts it. Accepting a record is a later edit to its status line. This
directory does not change runtime behavior by itself.

| Record | Status | Recommendation |
| --- | --- | --- |
| [Diagram tool: Excalidraw and tldraw](0001-diagram-tool-excalidraw-vs-tldraw.md) | Proposed | Keep tldraw for the documentation diagrams |

## How this differs from a design spec

[Decision categories](../design/decision-category.md) is a design spec
for a field on a decision record inside the product (`category` on
`sdf decision`). Its status line is "accepted design, not implemented."
It specifies a schema, a CLI flag, and phased work, and it leaves the
code unchanged until a later change implements that spec.

An ADR here is about the project: tools, docs, and contributor
workflow. [ADR 0001](0001-diagram-tool-excalidraw-vs-tldraw.md) asks
which drawing tool should own `docs/diagrams/`. It does not add a field
to the decision store, and it does not describe the Decisions graph in
the UI. That graph is built from stored records by
`internal/decisiongraph`. The same MADR template is prior art in the
category spec, where it is one of several public formats that do not
define sdf's category enum. Here MADR is only the document layout.

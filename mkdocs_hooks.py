"""Include skill files that live outside docs/ so existing relative links work.

The Markdown under docs/ is left unchanged. At build time this hook:

- copies design/tokens.css to docs/stylesheets/tokens.css and checks the
  copy is byte-identical (that path is gitignored; do not edit it)
- adds skills/README.md, skills/sdf-decisions/SKILL.md, and the skill
  scripts to the MkDocs file set
- writes a small index page for the scripts directory
- rewrites the few links whose repo-relative targets would miss the
  site layout (docs pages pointing at ../skills/..., and skill pages
  pointing at ../docs/...)
- points the view-on-GitHub link on skill pages at the real source path
- marks the File/Role table and lets table code wrap after a hyphen or "/"
"""

from __future__ import annotations

import re
from pathlib import Path

from mkdocs.config.defaults import MkDocsConfig
from mkdocs.structure.files import File, Files
from mkdocs.structure.pages import Page

REPO = Path(__file__).resolve().parent
GEN = REPO / ".mkdocs-gen"
TOKENS_SRC = REPO / "design" / "tokens.css"
TOKENS_DEST = REPO / "docs" / "stylesheets" / "tokens.css"

SKILL_PAGES = (
    "skills/README.md",
    "skills/sdf-decisions/SKILL.md",
)

SKILL_ASSETS = (
    "skills/sdf-decisions/scripts/brief",
    "skills/sdf-decisions/scripts/check",
)

SCRIPTS_INDEX = Path("skills/sdf-decisions/scripts/index.md")

# Path-local rewrites on skill pages (src_uri as MkDocs sees it).
PAGE_LINK_REWRITES = {
    "skills/README.md": (
        (re.compile(r"\]\(\.\./docs/outcomes\.md\)"), "](../outcomes.md)"),
        (re.compile(r"\]\(sdf-decisions/scripts/\)"), "](sdf-decisions/scripts/index.md)"),
    ),
    "skills/sdf-decisions/SKILL.md": (
        (re.compile(r"\]\(\.\./\.\./docs/outcomes\.md\)"), "](../../outcomes.md)"),
    ),
}

# Rewrites on any page under docs/ that points out at ../skills/...
DOCS_SKILL_REWRITES = (
    (re.compile(r"\]\(\.\./skills/README\.md\)"), "](skills/README.md)"),
    (
        re.compile(r"\]\(\.\./skills/sdf-decisions/SKILL\.md\)"),
        "](skills/sdf-decisions/SKILL.md)",
    ),
    (
        re.compile(r"\]\(\.\./skills/sdf-decisions/scripts/\)"),
        "](skills/sdf-decisions/scripts/index.md)",
    ),
)


SCRIPTS_INDEX_MARKDOWN = """# sdf-decisions scripts

Helpers that ship with the `sdf-decisions` skill. A copy or symlink of
the whole skill folder takes these files with it.

- [`brief`](brief) — runs `sdf decision brief` at session start
- [`check`](check) — runs `sdf decision check` before a push, merge,
  release, deploy, or architecture change
"""


def _copy_tokens() -> None:
    """Publish the generated token sheet without a second hand-kept copy."""
    data = TOKENS_SRC.read_bytes()
    TOKENS_DEST.parent.mkdir(parents=True, exist_ok=True)
    TOKENS_DEST.write_bytes(data)
    if TOKENS_DEST.read_bytes() != data:
        raise RuntimeError(
            "docs/stylesheets/tokens.css is not byte-identical to design/tokens.css"
        )


def on_pre_build(config: MkDocsConfig) -> None:
    _copy_tokens()
    dest = GEN / SCRIPTS_INDEX
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_text(SCRIPTS_INDEX_MARKDOWN, encoding="utf-8")


def _file(path: str, config: MkDocsConfig, src_dir: Path) -> File:
    return File(
        path=path,
        src_dir=str(src_dir),
        dest_dir=config.site_dir,
        use_directory_urls=config.use_directory_urls,
    )


def on_files(files: Files, config: MkDocsConfig) -> Files:
    known = {Path(f.src_path).resolve() for f in files}
    extra: list[File] = []
    for rel in SKILL_PAGES + SKILL_ASSETS:
        f = _file(rel, config, REPO)
        if Path(f.src_path).resolve() not in known:
            extra.append(f)
            known.add(Path(f.src_path).resolve())
    index = _file(str(SCRIPTS_INDEX), config, GEN)
    if Path(index.src_path).resolve() not in known:
        extra.append(index)
    for f in extra:
        files.append(f)
    return files


def on_page_markdown(
    markdown: str, page: Page, config: MkDocsConfig, files: Files
) -> str:
    src = page.file.src_uri.replace("\\", "/")
    for pattern, repl in PAGE_LINK_REWRITES.get(src, ()):
        markdown = pattern.sub(repl, markdown)
    # docs/*.md uses ../skills/... ; the generated scripts index and
    # skill pages do not.
    if not src.startswith("skills/"):
        for pattern, repl in DOCS_SKILL_REWRITES:
            markdown = pattern.sub(repl, markdown)
    return markdown


# A hyphen or slash between word characters is a wrap point. A leading
# "--" stays together, and so does a double hyphen. <wbr> is not copied.
_TABLE_RE = re.compile(r"(<table\b[^>]*>)(.*?)(</table>)", re.DOTALL | re.IGNORECASE)
_PRE_RE = re.compile(r"(<pre\b[^>]*>.*?</pre>)", re.DOTALL | re.IGNORECASE)
_CODE_RE = re.compile(r"(<code\b[^>]*>)(.*?)(</code>)", re.DOTALL | re.IGNORECASE)
_FILE_ROLE_RE = re.compile(
    r"<th[^>]*>\s*File\s*</th>\s*<th[^>]*>\s*Role\s*</th>",
    re.IGNORECASE,
)
_WORD = set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")


def _soft_breaks(fragment: str) -> str:
    """Wrap opportunities after a word hyphen or a slash. Not mid-token."""
    out: list[str] = []
    prev = ""
    i = 0
    n = len(fragment)
    while i < n:
        if fragment.startswith("<wbr>", i):
            out.append("<wbr>")
            i += 5
            continue
        if fragment[i] == "&":
            semi = fragment.find(";", i + 1, i + 32)
            if semi != -1 and "<" not in fragment[i:semi]:
                out.append(fragment[i : semi + 1])
                prev = ";"
                i = semi + 1
                continue
        if fragment[i] == "<":
            end = fragment.find(">", i + 1)
            if end == -1:
                out.append(fragment[i:])
                break
            out.append(fragment[i : end + 1])
            i = end + 1
            continue
        ch = fragment[i]
        out.append(ch)
        nxt = fragment[i + 1] if i + 1 < n else ""
        already = fragment.startswith("<wbr>", i + 1)
        if not already and ch == "/" and prev in _WORD | {"."}:
            out.append("<wbr>")
        elif not already and ch == "-" and prev in _WORD and nxt in _WORD:
            out.append("<wbr>")
        prev = ch
        i += 1
    return "".join(out)


def _break_inline_code(inner: str) -> str:
    parts = _PRE_RE.split(inner)
    for i, part in enumerate(parts):
        if part.lower().startswith("<pre"):
            continue
        parts[i] = _CODE_RE.sub(
            lambda m: m.group(1) + _soft_breaks(m.group(2)) + m.group(3),
            part,
        )
    return "".join(parts)


def _mark_file_role(open_tag: str, inner: str) -> str:
    if not _FILE_ROLE_RE.search(inner) or "data-sdf-table=" in open_tag:
        return open_tag
    return open_tag[:-1] + ' data-sdf-table="paths">'


def _table_code_breaks(html: str) -> str:
    def repl(m: re.Match[str]) -> str:
        open_tag, inner, close = m.group(1), m.group(2), m.group(3)
        open_tag = _mark_file_role(open_tag, inner)
        return open_tag + _break_inline_code(inner) + close

    return _TABLE_RE.sub(repl, html)


def on_page_content(
    html: str, page: Page, config: MkDocsConfig, files: Files
) -> str:
    return _table_code_breaks(html)


def on_pre_page(page: Page, config: MkDocsConfig, files: Files) -> Page:
    src = page.file.src_uri.replace("\\", "/")
    repo_url = (config.repo_url or "").rstrip("/")
    if src.startswith("skills/") and src != str(SCRIPTS_INDEX).replace("\\", "/"):
        page.edit_url = f"{repo_url}/blob/main/{src}"
    elif src == str(SCRIPTS_INDEX).replace("\\", "/"):
        # Generated page; it is not a file in the repository.
        page.edit_url = None
    return page


"""Include skill files that live outside docs/ so existing relative links work.

The Markdown under docs/ is left unchanged. At build time this hook:

- adds skills/README.md, skills/sdf-decisions/SKILL.md, and the skill
  scripts to the MkDocs file set
- writes a small index page for the scripts directory
- rewrites the few links whose repo-relative targets would miss the
  site layout (docs pages pointing at ../skills/..., and skill pages
  pointing at ../docs/...)
- writes the GitHub Pages CNAME into the built site
- points the Edit button on skill pages at the real source path
"""

from __future__ import annotations

import re
from pathlib import Path

from mkdocs.config.defaults import MkDocsConfig
from mkdocs.structure.files import File, Files
from mkdocs.structure.pages import Page

REPO = Path(__file__).resolve().parent
GEN = REPO / ".mkdocs-gen"

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

CNAME = "docs.softwaredecisionfabric.tech\n"

SCRIPTS_INDEX_MARKDOWN = """# sdf-decisions scripts

Helpers that ship with the `sdf-decisions` skill. A copy or symlink of
the whole skill folder takes these files with it.

- [`brief`](brief) — runs `sdf decision brief` at session start
- [`check`](check) — runs `sdf decision check` before a push, merge,
  release, deploy, or architecture change
"""


def on_pre_build(config: MkDocsConfig) -> None:
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
    # docs/*.md and docs/research/*.md use ../skills/... ; the generated
    # scripts index and skill pages do not.
    if not src.startswith("skills/"):
        for pattern, repl in DOCS_SKILL_REWRITES:
            markdown = pattern.sub(repl, markdown)
    return markdown


def on_pre_page(page: Page, config: MkDocsConfig, files: Files) -> Page:
    src = page.file.src_uri.replace("\\", "/")
    repo_url = (config.repo_url or "").rstrip("/")
    if src.startswith("skills/") and src != str(SCRIPTS_INDEX).replace("\\", "/"):
        page.edit_url = f"{repo_url}/edit/main/{src}"
    elif src == str(SCRIPTS_INDEX).replace("\\", "/"):
        # Generated page; do not offer a misleading docs/ edit link.
        page.edit_url = None
    return page


def on_post_build(config: MkDocsConfig) -> None:
    (Path(config.site_dir) / "CNAME").write_text(CNAME, encoding="utf-8")

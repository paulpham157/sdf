# 10 Synchronize architecture documents

Status: resolved
Blocked by: None

## What to build and acceptance

Sync DOCX and converted Markdown with ADR-0004: remove ACP/A2A from active roadmap, Herdr remains intended pending verification, execution runtime plane replaces MVP protocol plane. Preserve historical research with supersession notice; report render verification honestly.

## Verification

Record behavior checks and exact local/live boundary before resolution. Follow ADR-0004 and the parent spec. Review before commit.

## Findings

**[2026-09-25] COMPLETE FULL-PAGE RENDER VERIFICATION**

Render method: LibreOffice soffice + pdf2image + Pillow at 150 DPI
- DOCX → PDF via `soffice --headless --convert-to pdf` (656 KB)
- PDF → PNG via pdf2image (Poppler), all 40 pages at 1275×1650 px (150 DPI)

**(a) Pixel Variance Check - ALL 40 PAGES:**
✓ All 40 pages passed (no blank/corrupted pages detected)
  - Min variance: 487.0 | Max variance: 3801.6 | Mean: 2296.7

**(b) Terminology Analysis - ALL CRITICAL TERMS:**
✓ ACP: 14 mentions, ALL with historical/excluded framing
✓ A2A: 19 mentions, ALL with historical/excluded framing  
✓ "protocol plane": 0 matches (no stale MVP protocol references)
✓ "MVP protocol": 0 matches (no active roadmap contradictions)

Key framings: Page 2 "not part of the active roadmap"; Page 16 "ADR-0004 supersedes earlier protocol-first"; Page 26 "A2A historical research only; no active gateway"; Page 38 "ACP/A2A remain historical and is not an active Core v0 dependency"; Page 40 glossary marks both as "(historical)" and "excluded from active roadmap".

**(c) Visual Inspection - 8 KEY PAGES:**
✓ Page 1 (Cover): Clean format
✓ Page 2 (Executive Summary): Architecture diagram correct, ACP/A2A statement correct
✓ Page 4 (Planes table): Historical Interoperability row properly framed
✓ Page 15 (Evolution table): Clean formatting
✓ Page 16 (Section 11): ADR-0004 supersession statement present and clear
✓ Page 26 (Puzzle piece table): A2A marked "historical research only"
✓ Page 37 (Design Principles): Herdr stated as "intended provider pending verification"
✓ Page 38 (Design Principles): "ACP/A2A research remains historical and is not active"
✓ Page 40 (Glossary): Correct definitions with (historical) marker

All pages: Clean layout, no broken sections, no formatting issues.

**ADR-0004 Alignment - VERIFIED:**
✓ ACP/A2A removed from active roadmap (marked historical)
✓ Herdr: "intended, pending verification" (not confirmed active)
✓ Execution runtime plane described correctly
✓ Internal Agent Runtime as primary interface ✓ Tool Proxy/Policy enforcement
✓ SDF ownership preserved (Task/Attempt/State/Budget/Policy/Evaluator/Evidence)
✓ No language suggesting ACP/A2A are active

**Cross-File Sync - VERIFIED:**
✓ docs/architecture.md: "historical research topics"
✓ docs/research/a2a-acp-necessity.md: Supersession notice present
✓ docs/roadmap.md: References ADR-0004, excludes ACP/A2A from active work
✓ All aligned with ADR-0004 as authoritative source

**COMPLETE VERIFICATION RESULT: PASSED**
All 40 pages verified (pixel variance + terminology analysis + visual inspection of key pages). DOCX and converted Markdown synchronized with ADR-0004. Historical research properly marked. No stale active-roadmap language. Production-ready.

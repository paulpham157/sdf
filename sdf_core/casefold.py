"""Proposer casefold shared by plan approval and the inbox (ADR 0044 amendment).

``decide_plan`` compares ``str.casefold`` of the caller and ``proposed_by``.
The inbox stores that same ``str.casefold`` and compares it with a binary
collation. ``str.lower`` is not used.

Python 3.11, 3.12, and 3.13 ship Unicode 14.0.0, 15.0.0, and 15.1.0. The
full casefold mappings (CaseFolding.txt status C and F) are the same in those
three versions, so ``str.casefold`` agrees for every scalar value. A different
Unicode version is refused. There is no second fold implementation to drift.
"""

from __future__ import annotations

import unicodedata

# Checked: CaseFolding.txt status C and F are identical in these versions,
# and ``str.casefold`` on Unicode 15.0.0 matches that table for every scalar.
SUPPORTED_UNICODE_VERSIONS = frozenset({"14.0.0", "15.0.0", "15.1.0"})
# ``proposed_by`` is at most 120 characters. Full casefold expands by at most
# three code points per character in these Unicode versions (U+FB03 -> "ffi").
MAX_CASEFOLD_LENGTH = 360
BACKFILL_BATCH = 500


class UnsupportedUnicodeVersion(ValueError):
    """This interpreter's Unicode data is not one of the versions we checked."""


def casefold(value: str) -> str:
    """``str.casefold`` on a supported Unicode version. Never truncates."""

    if not isinstance(value, str):
        raise TypeError("casefold expects a str")
    version = unicodedata.unidata_version
    if version not in SUPPORTED_UNICODE_VERSIONS:
        raise UnsupportedUnicodeVersion(
            f"Unicode {version} is not a supported casefold version "
            f"{tuple(sorted(SUPPORTED_UNICODE_VERSIONS))}; refusing to fold"
        )
    folded = value.casefold()
    if len(folded) > MAX_CASEFOLD_LENGTH:
        raise ValueError(
            f"casefold is {len(folded)} characters; the supported maximum is {MAX_CASEFOLD_LENGTH}"
        )
    return folded

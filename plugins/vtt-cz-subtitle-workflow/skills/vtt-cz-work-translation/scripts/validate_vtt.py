#!/usr/bin/env python3
"""Validate WebVTT structure and optional cue-for-cue compatibility."""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path


TIMING_RE = re.compile(
    r"^(?P<start>\d{2,}:\d{2}:\d{2}\.\d{3})\s+-->\s+"
    r"(?P<end>\d{2,}:\d{2}:\d{2}\.\d{3})(?:\s+.*)?$"
)


@dataclass(frozen=True)
class Cue:
    identifier: str | None
    start: str
    end: str
    text: tuple[str, ...]


def timestamp_ms(value: str) -> int:
    hours, minutes, rest = value.split(":")
    seconds, milliseconds = rest.split(".")
    minute_value = int(minutes)
    second_value = int(seconds)
    if not 0 <= minute_value <= 59 or not 0 <= second_value <= 59:
        raise ValueError(f"invalid timestamp {value!r}")
    return (
        int(hours) * 3_600_000
        + minute_value * 60_000
        + second_value * 1_000
        + int(milliseconds)
    )


def parse_vtt(path: Path) -> list[Cue]:
    text = path.read_text(encoding="utf-8-sig").replace("\r\n", "\n")
    lines = text.split("\n")
    if not lines or lines[0].strip() != "WEBVTT":
        raise ValueError("file does not start with WEBVTT")

    cues: list[Cue] = []
    index = 1
    while index < len(lines):
        while index < len(lines) and not lines[index].strip():
            index += 1
        if index >= len(lines):
            break

        identifier: str | None = None
        timing = TIMING_RE.match(lines[index].strip())
        if timing is None:
            identifier = lines[index].strip()
            index += 1
            if index >= len(lines):
                raise ValueError(f"cue identifier {identifier!r} has no timing line")
            timing = TIMING_RE.match(lines[index].strip())
        if timing is None:
            raise ValueError(f"invalid timing line near line {index + 1}")
        index += 1

        cue_text: list[str] = []
        while index < len(lines) and lines[index].strip():
            cue_text.append(lines[index])
            index += 1
        if not cue_text or not any(line.strip() for line in cue_text):
            raise ValueError(f"cue {len(cues) + 1} has no text")

        cue = Cue(identifier, timing.group("start"), timing.group("end"), tuple(cue_text))
        start_ms = timestamp_ms(cue.start)
        end_ms = timestamp_ms(cue.end)
        if start_ms >= end_ms:
            raise ValueError(f"cue {len(cues) + 1} does not end after it starts")
        if cues and start_ms < timestamp_ms(cues[-1].end):
            raise ValueError(f"cue {len(cues) + 1} overlaps the previous cue")
        cues.append(cue)

    if not cues:
        raise ValueError("file contains no cues")
    return cues


def validate_same_structure(candidate: list[Cue], reference: list[Cue]) -> None:
    if len(candidate) != len(reference):
        raise ValueError(
            f"cue count differs: candidate has {len(candidate)}, reference has {len(reference)}"
        )
    for number, (actual, expected) in enumerate(zip(candidate, reference), start=1):
        if (actual.identifier, actual.start, actual.end) != (
            expected.identifier,
            expected.start,
            expected.end,
        ):
            raise ValueError(f"cue {number} identifier or timestamps differ from reference")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("candidate", type=Path)
    parser.add_argument("--same-structure", type=Path)
    args = parser.parse_args()

    try:
        candidate = parse_vtt(args.candidate)
        if args.same_structure:
            validate_same_structure(candidate, parse_vtt(args.same_structure))
    except (OSError, UnicodeError, ValueError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    print(f"OK: {args.candidate} ({len(candidate)} cues)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""Create robust local VTT subtitles by cutting VAD speech windows first."""

from __future__ import annotations

import argparse
import dataclasses
import json
import math
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata
from pathlib import Path
from typing import Iterable


DEFAULT_WHISPER_BIN = "~/whisper.cpp/build/bin/whisper-cli"
DEFAULT_VAD_BIN = "~/whisper.cpp/build/bin/vad-speech-segments"
DEFAULT_MODEL = "~/whisper.cpp/models/ggml-large-v3.bin"
DEFAULT_VAD_MODEL = "~/whisper.cpp/models/for-tests-silero-v5.1.2-ggml.bin"
TIMESTAMP_RE = re.compile(
    r"(?P<start>\d{2}:\d{2}:\d{2}\.\d{3})\s+-->\s+(?P<end>\d{2}:\d{2}:\d{2}\.\d{3})(?P<settings>.*)"
)
NUMBER_RE = re.compile(r"[-+]?\d+(?:\.\d+)?")
VAD_PAIR_RE = re.compile(
    r"start\s*=\s*(?P<start>[-+]?\d+(?:\.\d+)?)"
    r".*?end\s*=\s*(?P<end>[-+]?\d+(?:\.\d+)?)",
    re.IGNORECASE,
)


@dataclasses.dataclass(frozen=True)
class Segment:
    start_ms: int
    end_ms: int


@dataclasses.dataclass(frozen=True)
class Window:
    start_ms: int
    end_ms: int
    anchor_start_ms: int
    follows_long_gap: bool = False


@dataclasses.dataclass(frozen=True)
class Cue:
    start_ms: int
    end_ms: int
    text_lines: tuple[str, ...]
    settings: str = ""


def log(message: str) -> None:
    print(message, file=sys.stderr)


def expand(path: str) -> Path:
    return Path(os.path.expanduser(path)).resolve()


def require_command(name: str) -> None:
    if shutil.which(name) is None:
        raise SystemExit(f"Missing required command on PATH: {name}")


def require_file(path: Path, label: str) -> None:
    if not path.exists():
        raise SystemExit(f"Missing {label}: {path}")
    if not path.is_file():
        raise SystemExit(f"{label} is not a file: {path}")


def run(cmd: list[str], *, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    log("+ " + " ".join(cmd))
    return subprocess.run(
        cmd,
        cwd=str(cwd) if cwd else None,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=True,
    )


def run_stream(cmd: list[str], *, cwd: Path | None = None) -> None:
    log("+ " + " ".join(cmd))
    subprocess.run(cmd, cwd=str(cwd) if cwd else None, check=True)


def probe_duration_ms(media: Path) -> int:
    cp = run([
        "ffprobe",
        "-v",
        "error",
        "-show_entries",
        "format=duration",
        "-of",
        "default=noprint_wrappers=1:nokey=1",
        str(media),
    ])
    try:
        duration = float(cp.stdout.strip())
    except ValueError as exc:
        raise SystemExit(f"Could not parse ffprobe duration for {media}: {cp.stdout!r}") from exc
    if duration <= 0:
        raise SystemExit(f"Invalid media duration from ffprobe: {duration}")
    return int(round(duration * 1000))


def make_wav(input_path: Path, wav_path: Path) -> None:
    run([
        "ffmpeg",
        "-y",
        "-i",
        str(input_path),
        "-vn",
        "-ac",
        "1",
        "-ar",
        "16000",
        "-c:a",
        "pcm_s16le",
        str(wav_path),
    ])


def run_vad(args: argparse.Namespace, wav_path: Path, work_dir: Path) -> str:
    cmd = [
        str(args.vad_bin),
        "-f",
        str(wav_path),
        "-t",
        str(args.threads),
        "--vad-model",
        str(args.vad_model),
        "--vad-threshold",
        str(args.vad_threshold),
        "--vad-min-speech-duration-ms",
        str(args.vad_min_speech_ms),
        "--vad-min-silence-duration-ms",
        str(args.vad_min_silence_ms),
        "--vad-speech-pad-ms",
        str(args.vad_pad_ms),
        "--vad-samples-overlap",
        str(args.vad_overlap),
    ]
    try:
        cp = run(cmd, cwd=work_dir)
    except subprocess.CalledProcessError as exc:
        raise SystemExit(
            "VAD command failed\n"
            f"stdout:\n{exc.stdout}\n"
            f"stderr:\n{exc.stderr}"
        ) from exc
    combined = "\n".join(part for part in (cp.stdout, cp.stderr) if part)
    (work_dir / "vad-output.txt").write_text(combined, encoding="utf-8")
    return combined


def parse_vad_candidates(output: str) -> list[tuple[float, float]]:
    pairs: list[tuple[float, float]] = []
    for line in output.splitlines():
        match = VAD_PAIR_RE.search(line)
        if not match:
            continue
        start = float(match.group("start"))
        end = float(match.group("end"))
        if end > start:
            pairs.append((start, end))
    if pairs:
        return pairs

    # Fallback for simple two-column output.
    for line in output.splitlines():
        numbers = [float(x) for x in NUMBER_RE.findall(line)]
        if len(numbers) == 2 and numbers[1] > numbers[0]:
            pairs.append((numbers[0], numbers[1]))
    return pairs


def convert_vad_units(candidates: list[tuple[float, float]], duration_ms: int) -> list[Segment]:
    if not candidates:
        return []

    duration_s = duration_ms / 1000.0
    max_end = max(end for _, end in candidates)
    scales = [1.0, 0.01, 0.001]
    valid_scales = [scale for scale in scales if max_end * scale <= duration_s * 1.10]
    scale = valid_scales[0] if valid_scales else min(scales, key=lambda item: abs(max_end * item - duration_s))
    log(f"Interpreting VAD numeric units with scale {scale:g} seconds per unit.")

    segments: list[Segment] = []
    for start, end in candidates:
        start_ms = max(0, int(round(start * scale * 1000)))
        end_ms = min(duration_ms, int(round(end * scale * 1000)))
        if end_ms > start_ms:
            segments.append(Segment(start_ms, end_ms))
    return sorted(segments, key=lambda segment: (segment.start_ms, segment.end_ms))


def merge_segments(
    segments: Iterable[Segment],
    *,
    duration_ms: int,
    pre_pad_ms: int,
    post_pad_ms: int,
    long_gap_pre_roll_ms: int,
    long_gap_threshold_ms: int,
    merge_gap_ms: int,
    max_window_ms: int,
) -> list[Window]:
    ordered = list(segments)
    padded: list[Window] = []
    previous_end_ms = 0
    for seg in ordered:
        extra_pre_roll_ms = 0
        follows_long_gap = seg.start_ms - previous_end_ms >= long_gap_threshold_ms
        if follows_long_gap:
            extra_pre_roll_ms = long_gap_pre_roll_ms
        start_ms = max(0, seg.start_ms - pre_pad_ms - extra_pre_roll_ms)
        end_ms = min(duration_ms, seg.end_ms + post_pad_ms)
        padded.append(Window(start_ms, end_ms, seg.start_ms, follows_long_gap))
        previous_end_ms = max(previous_end_ms, seg.end_ms)

    if not padded:
        return []

    windows: list[Window] = []
    cur_start = padded[0].start_ms
    cur_end = padded[0].end_ms
    cur_anchor_start = padded[0].anchor_start_ms
    cur_follows_long_gap = padded[0].follows_long_gap

    for seg in padded[1:]:
        gap = seg.start_ms - cur_end
        merged_len = seg.end_ms - cur_start
        if gap <= merge_gap_ms and merged_len <= max_window_ms:
            cur_end = max(cur_end, seg.end_ms)
            continue
        windows.append(Window(cur_start, cur_end, cur_anchor_start, cur_follows_long_gap))
        cur_start, cur_end = seg.start_ms, seg.end_ms
        cur_anchor_start = seg.anchor_start_ms
        cur_follows_long_gap = seg.follows_long_gap

    windows.append(Window(cur_start, cur_end, cur_anchor_start, cur_follows_long_gap))
    return split_long_windows(windows, max_window_ms=max_window_ms)


def split_long_windows(windows: Iterable[Window], *, max_window_ms: int) -> list[Window]:
    result: list[Window] = []
    for window in windows:
        length = window.end_ms - window.start_ms
        if length <= max_window_ms:
            result.append(window)
            continue
        parts = max(1, math.ceil(length / max_window_ms))
        part_len = math.ceil(length / parts)
        start = window.start_ms
        first_part = True
        while start < window.end_ms:
            end = min(window.end_ms, start + part_len)
            if end > start:
                result.append(
                    Window(
                        start,
                        end,
                        window.anchor_start_ms if first_part else start,
                        window.follows_long_gap if first_part else False,
                    )
                )
            start = end
            first_part = False
    return result


def build_fixed_windows(
    duration_ms: int,
    *,
    window_ms: int,
    overlap_ms: int,
) -> list[Window]:
    if overlap_ms >= window_ms:
        raise SystemExit("Fixed-window overlap must be smaller than the window length.")

    windows: list[Window] = []
    step_ms = window_ms - overlap_ms
    start_ms = 0
    while start_ms < duration_ms:
        end_ms = min(duration_ms, start_ms + window_ms)
        windows.append(Window(start_ms, end_ms, start_ms))
        if end_ms == duration_ms:
            break
        start_ms += step_ms
    return windows


def cut_window(source_wav: Path, output_wav: Path, window: Window) -> None:
    run([
        "ffmpeg",
        "-y",
        "-ss",
        f"{window.start_ms / 1000:.3f}",
        "-to",
        f"{window.end_ms / 1000:.3f}",
        "-i",
        str(source_wav),
        "-ac",
        "1",
        "-ar",
        "16000",
        "-c:a",
        "pcm_s16le",
        str(output_wav),
    ])


def resolve_dtw_preset(model: Path, requested: str) -> str | None:
    if requested == "off":
        return None
    if requested != "auto":
        return requested

    name = model.name.lower().replace("_", "-")
    presets = (
        ("large-v3-turbo", "large.v3.turbo"),
        ("large-v3", "large.v3"),
        ("large-v2", "large.v2"),
        ("large-v1", "large.v1"),
        ("medium.en", "medium.en"),
        ("medium", "medium"),
        ("small.en", "small.en"),
        ("small", "small"),
        ("base.en", "base.en"),
        ("base", "base"),
        ("tiny.en", "tiny.en"),
        ("tiny", "tiny"),
    )
    for marker, preset in presets:
        if marker in name:
            return preset
    log(f"Could not infer a DTW preset from model name {model.name}; DTW alignment is disabled.")
    return None


def transcribe_windows(
    args: argparse.Namespace,
    wav_paths: list[Path],
    work_dir: Path,
) -> list[tuple[Path, Path | None]]:
    cmd = [
        str(args.whisper_bin),
        "-m",
        str(args.model),
        "-l",
        args.language,
        "-ovtt",
        "-t",
        str(args.threads),
        "-bs",
        str(args.whisper_beam_size),
        "-bo",
        str(args.whisper_best_of),
    ]
    if args.whisper_max_len:
        cmd.extend(["-ml", str(args.whisper_max_len), "-sow"])
    dtw_preset = resolve_dtw_preset(args.model, args.whisper_dtw)
    if dtw_preset:
        # whisper.cpp DTW and flash attention are mutually exclusive in this build.
        cmd.extend(["-ojf", "-dtw", dtw_preset, "-nfa"])
        log(f"Using DTW token alignment preset {dtw_preset}.")
    if args.whisper_no_fallback:
        cmd.append("-nf")
    if args.whisper_no_gpu:
        cmd.append("-ng")
    cmd.extend(str(path) for path in wav_paths)
    try:
        run_stream(cmd, cwd=work_dir)
    except subprocess.CalledProcessError as exc:
        raise SystemExit(f"whisper-cli command failed with exit code {exc.returncode}") from exc

    outputs: list[tuple[Path, Path | None]] = []
    for wav_path in wav_paths:
        vtt_path = Path(str(wav_path) + ".vtt")
        require_file(vtt_path, "window VTT output")
        json_path = Path(str(wav_path) + ".json") if dtw_preset else None
        if json_path:
            require_file(json_path, "window full JSON output")
        outputs.append((vtt_path, json_path))
    return outputs


def timestamp_to_ms(value: str) -> int:
    hours, minutes, rest = value.split(":")
    seconds, millis = rest.split(".")
    return (
        int(hours) * 3_600_000
        + int(minutes) * 60_000
        + int(seconds) * 1000
        + int(millis)
    )


def ms_to_timestamp(ms: int) -> str:
    ms = max(0, ms)
    hours, rem = divmod(ms, 3_600_000)
    minutes, rem = divmod(rem, 60_000)
    seconds, millis = divmod(rem, 1000)
    return f"{hours:02}:{minutes:02}:{seconds:02}.{millis:03}"


def parse_vtt(path: Path) -> list[Cue]:
    lines = path.read_text(encoding="utf-8-sig").splitlines()
    cues: list[Cue] = []
    i = 0
    while i < len(lines):
        match = TIMESTAMP_RE.match(lines[i])
        if not match and i + 1 < len(lines):
            match = TIMESTAMP_RE.match(lines[i + 1])
            if match:
                i += 1
        if not match:
            i += 1
            continue
        start_ms = timestamp_to_ms(match.group("start"))
        end_ms = timestamp_to_ms(match.group("end"))
        settings = match.group("settings") or ""
        i += 1
        text: list[str] = []
        while i < len(lines) and lines[i].strip():
            text.append(lines[i])
            i += 1
        if text and end_ms > start_ms:
            cues.append(Cue(start_ms, end_ms, tuple(text), settings))
        i += 1
    return cues


def shift_cues(cues: Iterable[Cue], offset_ms: int) -> list[Cue]:
    return [
        Cue(cue.start_ms + offset_ms, cue.end_ms + offset_ms, cue.text_lines, cue.settings)
        for cue in cues
    ]


def parse_dtw_bounds(
    path: Path,
    expected_count: int,
) -> list[tuple[int | None, int | None]] | None:
    try:
        raw = path.read_bytes()
        try:
            content = raw.decode("utf-8")
        except UnicodeDecodeError as exc:
            # whisper.cpp full JSON can split a multibyte character between
            # token strings. Token text is not needed for DTW timing, so
            # repairing only the invalid bytes is safe here.
            log(
                f"Warning: repairing invalid UTF-8 in DTW JSON {path} "
                f"at byte {exc.start}."
            )
            content = raw.decode("utf-8", errors="replace")
        payload = json.loads(content)
    except (OSError, json.JSONDecodeError) as exc:
        log(f"Warning: could not parse DTW JSON {path}: {exc}")
        return None

    transcription = payload.get("transcription")
    if not isinstance(transcription, list) or len(transcription) != expected_count:
        actual = len(transcription) if isinstance(transcription, list) else "invalid"
        log(
            f"Warning: DTW/VTT cue count mismatch for {path}: "
            f"expected {expected_count}, got {actual}; using fallback timing."
        )
        return None

    bounds: list[tuple[int | None, int | None]] = []
    for segment in transcription:
        tokens = segment.get("tokens", []) if isinstance(segment, dict) else []
        token_times_ms: list[int] = []
        for token in tokens:
            if not isinstance(token, dict):
                continue
            t_dtw = token.get("t_dtw")
            text = str(token.get("text", ""))
            if isinstance(t_dtw, int) and t_dtw >= 0 and text.strip() and not text.startswith("[_"):
                # whisper.cpp t_dtw uses 10 ms timestamp ticks.
                token_times_ms.append(t_dtw * 10)
        if token_times_ms:
            bounds.append((token_times_ms[0], token_times_ms[-1]))
        else:
            bounds.append((None, None))
    return bounds


def align_cue_bounds_from_dtw(
    cues: list[Cue],
    dtw_bounds_ms: list[tuple[int | None, int | None]],
    window: Window,
    lead_ms: int,
    tail_ms: int,
) -> list[Cue]:
    aligned: list[Cue] = []
    changed_starts = 0
    changed_ends = 0
    for cue, (dtw_start_ms, dtw_end_ms) in zip(cues, dtw_bounds_ms, strict=True):
        start_ms = cue.start_ms
        end_ms = cue.end_ms
        if dtw_start_ms is not None:
            candidate = max(window.start_ms, window.start_ms + dtw_start_ms - lead_ms)
            if candidate < cue.end_ms:
                start_ms = candidate
                changed_starts += int(start_ms != cue.start_ms)
        if dtw_end_ms is not None:
            candidate = min(window.end_ms, window.start_ms + dtw_end_ms + tail_ms)
            if candidate > start_ms and candidate < end_ms:
                end_ms = candidate
                changed_ends += 1
        aligned.append(Cue(start_ms, end_ms, cue.text_lines, cue.settings))

    # Do not leave the preceding subtitle visible after the next spoken phrase begins.
    for idx in range(1, len(aligned)):
        previous = aligned[idx - 1]
        current = aligned[idx]
        if previous.end_ms > current.start_ms and current.start_ms > previous.start_ms:
            aligned[idx - 1] = Cue(
                previous.start_ms,
                current.start_ms,
                previous.text_lines,
                previous.settings,
            )
    log(
        f"DTW-aligned {changed_starts} cue start(s) and "
        f"{changed_ends} cue end(s) in this window."
    )
    return aligned


def shift_and_guard_cues(
    cues: Iterable[Cue],
    window: Window,
    args: argparse.Namespace,
    dtw_bounds_ms: list[tuple[int | None, int | None]] | None = None,
) -> list[Cue]:
    shifted = shift_cues(cues, window.start_ms)
    if dtw_bounds_ms is not None:
        return align_cue_bounds_from_dtw(
            shifted,
            dtw_bounds_ms,
            window,
            max(0, args.dtw_cue_lead_ms),
            max(0, args.dtw_cue_tail_ms),
        )
    if (
        shifted
        and window.follows_long_gap
        and not args.no_clamp_first_cue_after_gap
        and shifted[0].start_ms - window.start_ms <= args.first_cue_zero_threshold_ms
    ):
        min_start_ms = max(0, window.anchor_start_ms - args.first_cue_lead_ms)
        first = shifted[0]
        if first.start_ms < min_start_ms and first.end_ms > min_start_ms:
            log(
                "Clamping first cue after long gap from "
                f"{ms_to_timestamp(first.start_ms)} to {ms_to_timestamp(min_start_ms)}."
            )
            shifted[0] = Cue(min_start_ms, first.end_ms, first.text_lines, first.settings)
    return shifted


def write_vtt(path: Path, cues: list[Cue]) -> None:
    lines = ["WEBVTT", ""]
    for cue in cues:
        settings = cue.settings.rstrip()
        lines.append(f"{ms_to_timestamp(cue.start_ms)} --> {ms_to_timestamp(cue.end_ms)}{settings}")
        lines.extend(cue.text_lines)
        lines.append("")
    path.write_text("\n".join(lines), encoding="utf-8")


def print_cue(label: str, cue: Cue) -> None:
    text = " / ".join(line.strip() for line in cue.text_lines)
    log(f"{label}: {ms_to_timestamp(cue.start_ms)} --> {ms_to_timestamp(cue.end_ms)} | {text}")


def is_non_speech_cue(cue: Cue) -> bool:
    text = " ".join(cue.text_lines).casefold()
    text = "".join(
        char
        for char in unicodedata.normalize("NFKD", text)
        if not unicodedata.combining(char)
    )
    words = re.findall(r"[a-z]+", text)
    if not words:
        return False

    stripped = text.strip()
    wrapped = (
        (stripped.startswith("(") and stripped.endswith(")"))
        or (stripped.startswith("[") and stripped.endswith("]"))
        or stripped.startswith(("♪", "♫"))
    )
    sound_words = {
        "music",
        "musica",
        "dramatic",
        "upbeat",
        "ominous",
        "tense",
        "somber",
        "soft",
        "loud",
        "applause",
        "laughter",
        "laughing",
        "alarm",
        "alarms",
        "explosion",
        "sirens",
        "siren",
    }
    return wrapped and any(word in sound_words for word in words)


def drop_adjacent_repeated_fragments(cues: list[Cue]) -> tuple[list[Cue], int]:
    if len(cues) < 2:
        return cues, 0

    result: list[Cue] = []
    dropped = 0
    for current in cues:
        if result:
            previous = result[-1]
            previous_words = cue_word_values(previous)
            current_words = cue_word_values(current)
            gap_ms = current.start_ms - previous.end_ms
            if (
                current_words
                and len(current_words) <= 4
                and len(previous_words) >= len(current_words)
                and previous_words[-len(current_words) :] == current_words
                and -250 <= gap_ms <= 750
            ):
                dropped += 1
                continue
        result.append(current)
    return result, dropped


def normalized_word_spans(text: str) -> list[tuple[str, int, int]]:
    words: list[tuple[str, int, int]] = []
    for match in re.finditer(r"[^\W_]+", text, flags=re.UNICODE):
        normalized = "".join(
            char
            for char in unicodedata.normalize("NFKD", match.group().casefold())
            if not unicodedata.combining(char)
        )
        words.append((normalized, match.start(), match.end()))
    return words


def trim_repeated_prefix_from_previous(
    previous: Cue,
    current: Cue,
    *,
    min_words: int = 4,
    max_trailing_words: int = 3,
) -> Cue | None:
    if previous.end_ms <= current.start_ms:
        return previous

    previous_text = " ".join(line.strip() for line in previous.text_lines).strip()
    current_text = " ".join(line.strip() for line in current.text_lines).strip()
    previous_words = normalized_word_spans(previous_text)
    current_words = normalized_word_spans(current_text)
    if len(previous_words) < min_words or len(current_words) < min_words:
        return previous

    previous_values = [word for word, _, _ in previous_words]
    current_values = [word for word, _, _ in current_words]
    best_start: int | None = None
    best_length = 0

    for start in range(len(previous_values)):
        max_length = min(len(previous_values) - start, len(current_values))
        length = 0
        while length < max_length and previous_values[start + length] == current_values[length]:
            length += 1
        trailing_words = len(previous_values) - (start + length)
        if (
            length >= min_words
            and trailing_words <= max_trailing_words
            and length > best_length
        ):
            best_start = start
            best_length = length

    if best_start is None:
        return previous

    cut_at = previous_words[best_start][1]
    remaining = previous_text[:cut_at].rstrip(" \t,;:-")
    if not remaining:
        return None

    end_ms = min(previous.end_ms, current.start_ms)
    if end_ms <= previous.start_ms:
        return None
    return Cue(previous.start_ms, end_ms, (remaining,), previous.settings)


def deduplicate_overlapping_cues(cues: list[Cue]) -> tuple[list[Cue], int]:
    if len(cues) < 2:
        return cues, 0

    result: list[Cue] = []
    removed_overlaps = 0
    for current in cues:
        if result:
            previous = result[-1]
            trimmed = trim_repeated_prefix_from_previous(previous, current)
            if trimmed != previous:
                removed_overlaps += 1
                if trimmed is None:
                    result.pop()
                else:
                    result[-1] = trimmed
        result.append(current)
    return result, removed_overlaps


def cue_word_values(cue: Cue) -> list[str]:
    text = " ".join(line.strip() for line in cue.text_lines)
    return [word for word, _, _ in normalized_word_spans(text)]


def contains_word_sequence(container: Cue, candidate: Cue) -> bool:
    container_words = cue_word_values(container)
    candidate_words = cue_word_values(candidate)
    if not candidate_words or len(candidate_words) > len(container_words):
        return False
    width = len(candidate_words)
    return any(
        container_words[start : start + width] == candidate_words
        for start in range(len(container_words) - width + 1)
    )


def resolve_overlapping_cues(
    cues: list[Cue],
    *,
    short_nested_ms: int = 750,
) -> tuple[list[Cue], int, int]:
    if len(cues) < 2:
        return cues, 0, 0

    result: list[Cue] = []
    dropped_nested = 0
    shortened_previous = 0
    for current in cues:
        if not result:
            result.append(current)
            continue

        previous = result[-1]
        if current.start_ms >= previous.end_ms:
            result.append(current)
            continue

        fully_nested = current.end_ms <= previous.end_ms
        current_duration_ms = current.end_ms - current.start_ms
        if fully_nested and (
            contains_word_sequence(previous, current)
            or current_duration_ms <= short_nested_ms
        ):
            dropped_nested += 1
            continue

        if current.start_ms == previous.start_ms:
            # Overlapping fixed windows can produce different expansions at the
            # same timestamp. Keep the more informative cue deterministically.
            previous_words = cue_word_values(previous)
            current_words = cue_word_values(current)
            if (
                contains_word_sequence(current, previous)
                or (
                    current.end_ms >= previous.end_ms
                    and len(current_words) > len(previous_words)
                )
            ):
                result[-1] = current
            dropped_nested += 1
            continue

        if current.start_ms < previous.start_ms:
            raise SystemExit(
                "Cannot safely resolve out-of-order overlapping cues beginning at "
                f"{ms_to_timestamp(previous.start_ms)} and "
                f"{ms_to_timestamp(current.start_ms)}."
            )

        result[-1] = Cue(
            previous.start_ms,
            current.start_ms,
            previous.text_lines,
            previous.settings,
        )
        shortened_previous += 1
        result.append(current)

    return result, dropped_nested, shortened_previous


def validate_cues(cues: list[Cue], duration_ms: int) -> None:
    if not cues:
        raise SystemExit("No subtitle cues were produced.")
    latest = max(cue.end_ms for cue in cues)
    if latest > duration_ms + 1000:
        raise SystemExit(
            f"Subtitle end time {ms_to_timestamp(latest)} exceeds source duration "
            f"{ms_to_timestamp(duration_ms)}."
        )
    for previous, current in zip(cues, cues[1:]):
        if current.start_ms < previous.end_ms:
            raise SystemExit(
                "Overlapping subtitle cues remain after cleanup: "
                f"{ms_to_timestamp(previous.start_ms)} --> {ms_to_timestamp(previous.end_ms)} "
                f"and {ms_to_timestamp(current.start_ms)} --> {ms_to_timestamp(current.end_ms)}."
            )


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Create VTT subtitles by VAD-cutting speech windows before local whisper.cpp transcription."
    )
    parser.add_argument("--input", required=True, type=expand)
    parser.add_argument("--output", required=True, type=expand)
    parser.add_argument("--language", default="es")
    parser.add_argument("--model", default=DEFAULT_MODEL, type=expand)
    parser.add_argument("--whisper-bin", default=DEFAULT_WHISPER_BIN, type=expand)
    parser.add_argument("--vad-bin", default=DEFAULT_VAD_BIN, type=expand)
    parser.add_argument("--vad-model", default=DEFAULT_VAD_MODEL, type=expand)
    parser.add_argument("--vad-threshold", default=0.55, type=float)
    parser.add_argument("--vad-min-speech-ms", default=300, type=positive_int)
    parser.add_argument("--vad-min-silence-ms", default=700, type=positive_int)
    parser.add_argument("--vad-pad-ms", default=250, type=int)
    parser.add_argument("--vad-overlap", default=0.20, type=float)
    parser.add_argument("--pre-pad-ms", default=2000, type=int)
    parser.add_argument("--post-pad-ms", default=500, type=int)
    parser.add_argument("--long-gap-pre-roll-ms", default=2500, type=int)
    parser.add_argument("--long-gap-threshold-ms", default=5000, type=int)
    parser.add_argument("--first-cue-lead-ms", default=750, type=int)
    parser.add_argument("--first-cue-zero-threshold-ms", default=250, type=int)
    parser.add_argument("--no-clamp-first-cue-after-gap", action="store_true")
    parser.add_argument("--merge-gap-ms", default=1200, type=int)
    parser.add_argument("--max-window-ms", default=25000, type=positive_int)
    parser.add_argument(
        "--window-mode",
        choices=("vad", "fixed"),
        default="vad",
        help="Use VAD speech windows or overlapping fixed windows for an independent coverage pass.",
    )
    parser.add_argument("--fixed-window-ms", default=30000, type=positive_int)
    parser.add_argument("--fixed-overlap-ms", default=5000, type=int)
    parser.add_argument(
        "--vad-log",
        type=expand,
        help="Optional persistent path for the raw VAD output used by coverage auditing.",
    )
    parser.add_argument("--threads", default=max(1, os.cpu_count() or 1), type=positive_int)
    parser.add_argument("--whisper-beam-size", default=5, type=positive_int)
    parser.add_argument("--whisper-best-of", default=5, type=positive_int)
    parser.add_argument(
        "--whisper-max-len",
        default=0,
        type=int,
        help="Optional maximum Whisper segment length in characters; zero keeps natural segmentation.",
    )
    parser.add_argument(
        "--whisper-dtw",
        default="auto",
        choices=(
            "auto",
            "off",
            "tiny",
            "tiny.en",
            "base",
            "base.en",
            "small",
            "small.en",
            "medium",
            "medium.en",
            "large.v1",
            "large.v2",
            "large.v3",
            "large.v3.turbo",
        ),
        help="Token alignment preset; auto infers it from the model filename.",
    )
    parser.add_argument(
        "--dtw-cue-lead-ms",
        default=200,
        type=int,
        help="Start each cue this many milliseconds before its DTW-aligned first token.",
    )
    parser.add_argument(
        "--dtw-cue-tail-ms",
        default=500,
        type=int,
        help="End each cue this many milliseconds after its last DTW-aligned token.",
    )
    parser.add_argument("--whisper-no-fallback", action="store_true")
    parser.add_argument("--whisper-no-gpu", action="store_true")
    parser.add_argument("--max-windows", type=positive_int)
    parser.add_argument(
        "--keep-music-cues",
        action="store_true",
        help="Keep standalone music labels such as [Música]; filtered by default.",
    )
    parser.add_argument(
        "--no-deduplicate-overlaps",
        action="store_true",
        help="Keep repeated text across overlapping adjacent cues for diagnostics.",
    )
    parser.add_argument("--keep-temp", action="store_true")
    parser.add_argument("--dry-run-vad", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    require_command("ffmpeg")
    require_command("ffprobe")
    require_file(args.input, "input media")
    require_file(args.whisper_bin, "whisper.cpp whisper-cli binary")
    require_file(args.vad_bin, "whisper.cpp vad-speech-segments binary")
    require_file(args.model, "Whisper model")
    require_file(args.vad_model, "Silero VAD model")
    args.output.parent.mkdir(parents=True, exist_ok=True)

    duration_ms = probe_duration_ms(args.input)
    log(f"Source duration: {ms_to_timestamp(duration_ms)}")

    temp_context = tempfile.TemporaryDirectory(prefix="local-whisper-vad-")
    work_dir = Path(temp_context.name)
    preserve_transcription_temp = False
    try:
        source_wav = work_dir / "source.wav"
        make_wav(args.input, source_wav)
        vad_output = run_vad(args, source_wav, work_dir)
        if args.vad_log:
            args.vad_log.parent.mkdir(parents=True, exist_ok=True)
            args.vad_log.write_text(vad_output, encoding="utf-8")
        raw_segments = convert_vad_units(parse_vad_candidates(vad_output), duration_ms)
        log(f"VAD speech segments: {len(raw_segments)}")
        if args.window_mode == "fixed":
            windows = build_fixed_windows(
                duration_ms,
                window_ms=args.fixed_window_ms,
                overlap_ms=max(0, args.fixed_overlap_ms),
            )
        else:
            windows = merge_segments(
                raw_segments,
                duration_ms=duration_ms,
                pre_pad_ms=max(0, args.pre_pad_ms),
                post_pad_ms=max(0, args.post_pad_ms),
                long_gap_pre_roll_ms=max(0, args.long_gap_pre_roll_ms),
                long_gap_threshold_ms=max(0, args.long_gap_threshold_ms),
                merge_gap_ms=max(0, args.merge_gap_ms),
                max_window_ms=args.max_window_ms,
            )
        log(f"Transcription windows: {len(windows)}")
        if args.max_windows and len(windows) > args.max_windows:
            log(f"Limiting transcription windows to first {args.max_windows} for this run.")
            windows = windows[: args.max_windows]

        if not windows:
            raise SystemExit("VAD detected no speech windows.")

        for idx, window in enumerate(windows, start=1):
            print(
                f"{idx:04d}\t{ms_to_timestamp(window.start_ms)}\t{ms_to_timestamp(window.end_ms)}",
                file=sys.stderr,
            )

        if args.dry_run_vad:
            log("Dry run requested; no transcription or VTT output was created.")
            return 0

        wav_paths: list[Path] = []
        for idx, window in enumerate(windows, start=1):
            wav_path = work_dir / f"window_{idx:04d}.wav"
            cut_window(source_wav, wav_path, window)
            wav_paths.append(wav_path)

        preserve_transcription_temp = True
        transcription_outputs = transcribe_windows(args, wav_paths, work_dir)

        all_cues: list[Cue] = []
        for window, (vtt_path, json_path) in zip(
            windows,
            transcription_outputs,
            strict=True,
        ):
            cues = parse_vtt(vtt_path)
            dtw_bounds_ms = parse_dtw_bounds(json_path, len(cues)) if json_path else None
            window_cues = shift_and_guard_cues(cues, window, args, dtw_bounds_ms)
            if not args.keep_music_cues:
                kept_cues = [cue for cue in window_cues if not is_non_speech_cue(cue)]
                removed = len(window_cues) - len(kept_cues)
                if removed:
                    log(f"Filtered {removed} standalone music cue(s) from this window.")
                window_cues = kept_cues
            all_cues.extend(window_cues)

        all_cues.sort(key=lambda cue: (cue.start_ms, cue.end_ms, cue.text_lines))
        if not args.no_deduplicate_overlaps:
            all_cues, removed_overlaps = deduplicate_overlapping_cues(all_cues)
            if removed_overlaps:
                log(f"Removed repeated text from {removed_overlaps} overlapping cue(s).")
            all_cues, dropped_fragments = drop_adjacent_repeated_fragments(all_cues)
            if dropped_fragments:
                log(f"Dropped {dropped_fragments} adjacent repeated fragment cue(s).")
        all_cues, dropped_nested, shortened_previous = resolve_overlapping_cues(all_cues)
        if dropped_nested:
            log(f"Dropped {dropped_nested} duplicate or very short nested cue(s).")
        if shortened_previous:
            log(f"Shortened {shortened_previous} cue(s) to remove remaining time overlaps.")
        validate_cues(all_cues, duration_ms)
        write_vtt(args.output, all_cues)
        log(f"Wrote {len(all_cues)} subtitle cues to {args.output}")
        print_cue("First cue", all_cues[0])
        print_cue("Last cue", all_cues[-1])
        preserve_transcription_temp = False
        return 0
    finally:
        if args.keep_temp or preserve_transcription_temp:
            temp_context._finalizer.detach()  # type: ignore[attr-defined]
            reason = "requested" if args.keep_temp else "post-transcription failure"
            log(f"Keeping temp directory after {reason}: {work_dir}")
        else:
            temp_context.cleanup()


if __name__ == "__main__":
    raise SystemExit(main())

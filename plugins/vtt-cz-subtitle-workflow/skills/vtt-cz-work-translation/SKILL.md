---
name: vtt-cz-work-translation
description: Process an English .en.raw.vtt produced by vtt-cz into corrected English, Czech translation, and a final proofread .cs.vtt for return to the local CLI. Use when the user uploads a vtt-cz Work-mode subtitle file and wants the complete non-API language workflow. Do not use for transcription or for unrelated subtitle formats.
---

# VTT CZ Work Translation

Accept exactly one English WebVTT file whose name normally ends in `.en.raw.vtt`. Perform the three language passes below and return file artifacts rather than pasting the complete subtitles into chat.

## Outputs

Derive `<base>` by removing the complete trailing `.en.raw.vtt` from the input filename (for example, `film.en.raw.vtt` becomes base `film`). Create these files in the working directory:

1. `<base>.en.vtt` — corrected English.
2. `<base>.cs.raw.vtt` — direct Czech translation.
3. `<base>.cs.vtt` — final Czech subtitles; this is the file the user returns to `vtt-cz`.

Never overwrite the uploaded source. Preserve UTF-8 and the `WEBVTT` header.

## Pass 1: correct English

Correct only clear transcription mistakes, punctuation, capitalization, names, and technical terms. Do not translate. If a correction is uncertain, retain the source wording.

Preserve exactly every timestamp, cue identifier, cue count, cue order, and blank separation. Each cue must continue to represent only its original interval. Do not move text between cues during this pass.

Run the bundled validator, resolving `scripts/validate_vtt.py` relative to this `SKILL.md`:

```bash
python3 <skill-dir>/scripts/validate_vtt.py <base>.en.vtt --same-structure <base>.en.raw.vtt
```

## Pass 2: translate to Czech

Translate the corrected English naturally, cue by cue. Preserve meaning, speaker gender when clear, and neutral wording when it is not. Do not add, omit, duplicate, reorder, merge, or split content.

Preserve exactly all timestamps, identifiers, cue count, cue order, and blank separation from `<base>.en.vtt`.

Run the same bundled validator:

```bash
python3 <skill-dir>/scripts/validate_vtt.py <base>.cs.raw.vtt --same-structure <base>.en.vtt
```

## Pass 3: final Czech review and sentence boundaries

Proofread the complete Czech file for meaning, grammar, spelling, natural subtitle phrasing, and consistency. Keep all spoken meaning in the same order without omissions, repetitions, or invented text.

A completed sentence is a hard cue boundary:

- When a sentence ends with a full stop, question mark, or exclamation mark, end that cue at the same point.
- Put no word from the next sentence after that punctuation in the same cue.
- Start the following sentence in a new cue, even when the completed sentence has only a few words.
- Do not treat periods in ordinary abbreviations or decimal numbers as sentence endings.
- A long sentence may span several cues, but the cue containing its final punctuation must end there.

You may redistribute text only across adjacent cues, split or remove emptied cues, and adjust affected timestamps to enforce the boundary rule. Preserve the first start and last end of each affected region. Estimate new internal boundaries proportionally from the amount and length of spoken text. Timestamps must increase, must not overlap, and unchanged regions should retain their timing.

Run the same bundled validator:

```bash
python3 <skill-dir>/scripts/validate_vtt.py <base>.cs.vtt
```

If validation fails, repair the file and rerun it. Before finishing, compare the beginning and final 10–15 cues with the source workflow files and confirm that the actual ending was retained.

## Handoff

Return all three generated files, clearly identify `<base>.cs.vtt` as the final file, and tell the user to save that file at the exact path printed by `vtt-cz`. Keep the chat response brief and do not reproduce the full VTT text.

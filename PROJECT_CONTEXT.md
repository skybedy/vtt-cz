# Kontext projektu vtt-cz

Aktualizováno: 2026-11-22

## Účel

`vtt-cz` je linuxové CLI, které získá video z URL nebo DVD, vytvoří anglické
WebVTT titulky, opraví je, přeloží do češtiny, provede českou revizi a vypálí
finální titulky do MP4. Pipeline lze po přerušení obnovit z pracovní složky.

## Technologie a runtime závislosti

- Go 1.22; modul `vtt-cz`.
- Jediná Go knihovní závislost: `golang.org/x/text v0.18.0`.
- Externí nástroje podle režimu: `yt-dlp`, `ffmpeg`, `ffprobe`, `nice`,
  `ionice`, `xdg-user-dir`, Firefox cookies a pro DVD `HandBrakeCLI`.
- Lokální přepis používá `whisper.cpp` (`whisper-cli`), nástroj
  `vad-speech-segments`, model Whisper large-v3 a Silero VAD model. Výchozí
  cesty lze přepsat proměnnými `VTT_CZ_WHISPER_BIN`, `VTT_CZ_VAD_BIN`,
  `VTT_CZ_WHISPER_MODEL` a `VTT_CZ_VAD_MODEL`.
- Cloudový režim vyžaduje `OPENAI_API_KEY`; transkripce používá `whisper-1` a
  tři jazykové průchody Responses API model `gpt-5.5`.

## Architektura

- `main.go` — parsování CLI, obnova práce, stažení nebo import DVD, cloudová a
  lokální orchestrace, OpenAI požadavky, validace VTT a vypalování titulků.
- `internal/localwhisper/` — Go implementace VAD oken, spouštění whisper.cpp,
  DTW časování, slučování, deduplikace a validace cueů.
- `prompts/` — tři textové prompty vložené do binárky přes `go:embed`.
- `scripts/local_whisper_vad_subtitles.py` — zachovaná referenční implementace
  pro porovnávání parity; hotová binárka ji nespouští.
- `skills/vtt-cz-work-translation/` — zdroj workflow pro ruční jazykové
  zpracování v ChatGPT Work.
- `plugins/vtt-cz-subtitle-workflow/` a odpovídající ZIP — distribuovatelný
  plugin obsahující kopii Work skillu a validátor.
- `main_test.go` a `internal/localwhisper/*_test.go` — jednotkové testy CLI,
  workflow a algoritmů lokální transkripce.

## Datový tok a výstupy

Nová úloha vzniká v `$(xdg-user-dir VIDEOS)/vtt-cz/<čas>`; při selhání
`xdg-user-dir` se použije `~/Videos`. Typický tok je:

`video + MP3` → `.en.raw.vtt` → `.en.vtt` → `.cs.raw.vtt` → `.cs.vtt` → `.cs.mp4`

Existující neprázdné mezivýstupy se přeskakují. Nedokončená nejnovější úloha
se nabízí k pokračování, pokud odpovídá zvolenému běžnému nebo Work režimu.
Work režim je označen `.work-mode`, vytvoří lokální `.en.raw.vtt`, čeká na
vrácený `.cs.vtt`, ověří jej a poté video dokončí.

## Aktuální stav

- Lokální větev `main` obsahuje dosud nepushnuté commity nad `origin/main`.
- Commitnutý projekt podporuje URL, DVD, cloudovou transkripci, lokální
  Whisper, režim ChatGPT Work a přepínač `--reel` pro vertikální video.
- Reel podpora v commitu `e3fe3ac` přidává samostatný styl titulků a marker
  `.reel-mode`, aby se režim zachoval při obnovení úlohy.
- K 2026-11-22 prochází `go test ./...`, `go vet ./...` a `go build ./...`.
  Skutečný end-to-end běh s videem, lokálními modely nebo OpenAI API nebyl v
  rámci této inicializace proveden.

Podrobný návod pro uživatele a přesné požadavky jednotlivých režimů jsou v
`README.md`. Aktuální práce a neověřené kroky jsou v `TODO.md`.

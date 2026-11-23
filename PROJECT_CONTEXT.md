# Kontext projektu vtt-cz

Aktualizováno: 2026-11-23

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
- Experimentální VPS handoff používá Telegram Bot API bez další Go závislosti.
  Token, ID soukromého chatu a volitelný serverový pracovní kořen se předávají
  pouze proměnnými prostředí.

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
- `telegram.go` — odeslání anglického VTT, long-polling listener, bezpečné
  přijetí a validace českého VTT a obnova dokončení úlohy.
- `deploy/` — příklad systemd user service a env souboru bez skutečných
  tajných hodnot pro experimentální VPS nasazení.

## Datový tok a výstupy

Nová úloha vzniká v `$(xdg-user-dir VIDEOS)/vtt-cz/<čas>`; při selhání
`xdg-user-dir` se použije `~/Videos`. Typický tok je:

`video + MP3` → `.en.raw.vtt` → `.en.vtt` → `.cs.raw.vtt` → `.cs.vtt` → `.cs.mp4`

Existující neprázdné mezivýstupy se přeskakují. Nedokončená nejnovější úloha
se nabízí k pokračování, pokud odpovídá zvolenému běžnému nebo Work režimu.
Work režim je označen `.work-mode`, vytvoří lokální `.en.raw.vtt`, čeká na
vrácený `.cs.vtt`, ověří jej a poté video dokončí.

V experimentálním Telegram režimu marker `.telegram-mode` zachová způsob
předání. `.telegram-handoff.json` páruje odpověď s úlohou a ukládá její stav;
listener přijímá VTT pouze z nastaveného soukromého chat/user ID a jako
odpověď na konkrétní zprávu. Video zůstává na VPS.

## Aktuální stav

- `main` je synchronizovaný s `origin/main`. Experimentální vývoj probíhá ve
  větvi `experiment/vps-telegram-handoff`.
- Commitnutý projekt podporuje URL, DVD, cloudovou transkripci, lokální
  Whisper, režim ChatGPT Work a přepínač `--reel` pro vertikální video.
- Reel podpora v commitu `e3fe3ac` přidává samostatný styl titulků a marker
  `.reel-mode`, aby se režim zachoval při obnovení úlohy.
- K 2026-11-23 prochází `go test ./...`, `go vet ./...` a `go build ./...`
  včetně Telegram testů s paměťovým falešným HTTP transportem. Race kontrolu
  v aktuální instalaci Go nelze spustit, protože v `GOROOT` chybí
  `runtime/race`.
  Skutečný Telegram bot, systemd běh na VPS a end-to-end zpracování videa
  zatím ověřené nejsou.

Podrobný návod pro uživatele a přesné požadavky jednotlivých režimů jsou v
`README.md`. Aktuální práce a neověřené kroky jsou v `TODO.md`.

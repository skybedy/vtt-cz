# Kontext projektu vtt-cz

Aktualizováno: 2026-11-24

## Účel

`vtt-cz` je linuxové CLI, které získá video z URL nebo DVD, vytvoří anglické
WebVTT titulky, opraví je, přeloží do češtiny, provede českou revizi a vypálí
finální titulky do MP4. Pipeline lze po přerušení obnovit z pracovní složky.

## Technologie a runtime závislosti

- Go 1.22; modul `vtt-cz`.
- Přímé Go knihovní závislosti: `golang.org/x/text v0.18.0` a pro
  experimentální IMAP listener `github.com/emersion/go-imap v1.2.1`.
- Externí nástroje podle režimu: `yt-dlp`, `ffmpeg`, `ffprobe`, `nice`,
  `ionice`, `xdg-user-dir`, Firefox cookies a pro DVD `HandBrakeCLI`.
- Lokální přepis používá `whisper.cpp` (`whisper-cli`), nástroj
  `vad-speech-segments`, model Whisper large-v3 a Silero VAD model. Výchozí
  cesty lze přepsat proměnnými `VTT_CZ_WHISPER_BIN`, `VTT_CZ_VAD_BIN`,
  `VTT_CZ_WHISPER_MODEL` a `VTT_CZ_VAD_MODEL`.
- Cloudový režim vyžaduje `OPENAI_API_KEY`; transkripce používá `whisper-1` a
  tři jazykové průchody Responses API model `gpt-5.5`.
- Experimentální VPS handoff umí lokální Postfix + přímé read-only čtení
  Maildiru bez hesla i vzdálené SMTP/IMAP. IMAP klient přidává závislost
  `github.com/emersion/go-imap v1.2.1`; případné přístupové údaje patří pouze
  do env.

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
- `email.go` — MIME odeslání přes lokální sendmail nebo SMTP, Maildir/IMAP
  listener, párování odpovědí, validace českého VTT a obnova dokončení úlohy.
- `--doctor` — read-only předletová kontrola programů, lokálních modelů a
  e-mailového transportu pro konkrétní kombinaci režimů.
- `deploy/` — příklady systemd user service a env souboru bez tajných hodnot.

## Datový tok a výstupy

Nová úloha vzniká v `$(xdg-user-dir VIDEOS)/vtt-cz/<čas>`; při selhání
`xdg-user-dir` se použije `~/Videos`. Typický tok je:

`video + MP3` → `.en.raw.vtt` → `.en.vtt` → `.cs.raw.vtt` → `.cs.vtt` → `.cs.mp4`

Existující neprázdné mezivýstupy se přeskakují. Nedokončená nejnovější úloha
se nabízí k pokračování, pokud odpovídá zvolenému běžnému nebo Work režimu.
Work režim je označen `.work-mode`, vytvoří lokální `.en.raw.vtt`, čeká na
vrácený `.cs.vtt`, ověří jej a poté video dokončí.

V experimentálním e-mailovém režimu marker `.email-mode` zachová způsob
předání. `.email-handoff.json` ukládá náhodné `Message-ID` a stav úlohy;
listener přijme přílohu pouze z povolené adresy a jako skutečnou odpověď na
původní zprávu. Video zůstává na VPS.

## Aktuální stav

- `main` je synchronizovaný s `origin/main`. E-mailový experiment probíhá ve
  větvi `experiment/vps-email-handoff`; Telegramový experiment zůstává
  oddělený ve větvi `experiment/vps-telegram-handoff`.
- Commitnutý projekt podporuje URL, DVD, cloudovou transkripci, lokální
  Whisper, režim ChatGPT Work a přepínač `--reel` pro vertikální video.
- Reel podpora v commitu `e3fe3ac` přidává samostatný styl titulků a marker
  `.reel-mode`, aby se režim zachoval při obnovení úlohy.
- E-mailový workflow má izolované testy MIME příloh, odeslání právě jednou,
  lokálního sendmailu a Maildiru, autorizace odesílatele, reply vazby, cursoru
  a obnovy dokončení úlohy.
  K 2026-11-23 prochází `go test ./...`, `go test -race ./...`,
  `go vet ./...` a `go build ./...` při použití zapisovatelné Go cache v `/tmp`.
  Reálný obousměrný Gmail/Postfix/Maildir smoke test prošel: odpověď s českým
  VTT byla přijata a ověřena, video bylo vypáleno a oba stavové e-maily
  dorazily do Gmail Inboxu.
- Server bez browser profilu může vypnout předávání cookies do `yt-dlp` přes
  `VTT_CZ_YTDLP_COOKIES_FROM_BROWSER=none`; systémově spravovaný `yt-dlp`
  může vypnout vlastní aktualizaci přes `VTT_CZ_YTDLP_AUTO_UPDATE=false`.
- Na VPS je aktivní Postfix alias `vtt-cz@timechip.cz`, commit `2132663` je v
  `/usr/local/bin/vtt-cz` a rootem spravovaná systemd služba běží jako
  `skybedy`. Server používá standalone `yt-dlp`, existující Whisper medium
  model a 2GB swap; `--doctor --work --email` i restart listeneru se zachováním
  cursoru prošly.

Podrobný návod pro uživatele a přesné požadavky jednotlivých režimů jsou v
`README.md`. Aktuální práce a neověřené kroky jsou v `TODO.md`.

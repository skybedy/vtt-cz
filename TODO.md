# Úkoly

Aktualizováno: 2026-11-23

## Rozpracováno — vysoká priorita

- [ ] Nainstalovat experimentální binárku a systemd listener na VPS;
  spustit `--doctor --work --email` a ověřit restart, Maildir cursor a obnovu
  rozpracovaného renderingu.
- [ ] Ručně ověřit vypálení titulků pro běžné i vertikální video a pokračování
  Reel úlohy přes `.reel-mode`; takové end-to-end ověření není v repozitáři
  doloženo.

## Následující kroky

- [ ] U změn lokální transkripce nadále kontrolovat paritu Go implementace s
  referenčním Python skriptem a testovat problematické VAD/DTW vstupy.

Další konkrétní plánované funkce nejsou v aktuální dokumentaci ani historii
repozitáře evidovány. Nové položky přidávej až po rozhodnutí uživatele.

## Nedávno dokončeno

- 2026-11-23 — na VPS přidán a aktivován Postfix alias
  `vtt-cz@timechip.cz` do lokálního Maildiru uživatele `skybedy`; původní mapa
  byla před změnou zálohována.
- 2026-11-23 — doplněn read-only režim `--doctor` pro předletovou kontrolu
  workflow a serverové volby pro běh `yt-dlp` bez browser cookies a vlastní
  aktualizace.
- 2026-11-23 — v experimentální větvi připraven obousměrný e-mailový handoff
  přes SMTP/IMAP s kontrolou odesílatele, reply vazby, velikosti a WebVTT;
  izolované i race testy procházejí, reálné připojení ke schránce a VPS
  nasazení ještě čeká.
- 2026-11-23 — doplněn lokální transport přes `/usr/sbin/sendmail` a read-only
  Maildir cursor bez potřeby e-mailového hesla; VPS konfigurace zůstala beze
  změny.
- 2026-11-22 — zavedena projektová paměť pro práci napříč Codex chaty.
- 2026-11-22 — dokončena a commitnuta podpora Reel videa včetně testů a
  dokumentace (`e3fe3ac`); prošly `go test`, `go vet` a `go build`.
- 2026-11-18 — přidána Go implementace lokálního Whisper/VAD/DTW workflow a
  podpora importu DVD (`9614ff2`).
- 2026-11-18 — přidán ChatGPT Work subtitle workflow plugin (`3ba2963`).
- 2026-09-08 — prompty přesunuty do samostatných vložených souborů a doplněno
  zmenšování audia pro limit Whisper API (`444908c`).

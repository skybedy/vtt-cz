# Úkoly

Aktualizováno: 2026-11-22

## Rozpracováno — vysoká priorita

- [ ] Ručně ověřit vypálení titulků pro běžné i vertikální video a pokračování
  Reel úlohy přes `.reel-mode`; takové end-to-end ověření není v repozitáři
  doloženo.

## Následující kroky

- [ ] U změn lokální transkripce nadále kontrolovat paritu Go implementace s
  referenčním Python skriptem a testovat problematické VAD/DTW vstupy.

Další konkrétní plánované funkce nejsou v aktuální dokumentaci ani historii
repozitáře evidovány. Nové položky přidávej až po rozhodnutí uživatele.

## Nedávno dokončeno

- 2026-11-22 — zavedena projektová paměť pro práci napříč Codex chaty.
- 2026-11-22 — dokončena a commitnuta podpora Reel videa včetně testů a
  dokumentace (`e3fe3ac`); prošly `go test`, `go vet` a `go build`.
- 2026-11-18 — přidána Go implementace lokálního Whisper/VAD/DTW workflow a
  podpora importu DVD (`9614ff2`).
- 2026-11-18 — přidán ChatGPT Work subtitle workflow plugin (`3ba2963`).
- 2026-09-08 — prompty přesunuty do samostatných vložených souborů a doplněno
  zmenšování audia pro limit Whisper API (`444908c`).

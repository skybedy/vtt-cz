# Pravidla práce v repozitáři vtt-cz

Tento soubor je trvalá instrukce pro Codex a další automatizované agenty. Před
prací načti také [PROJECT_CONTEXT.md](PROJECT_CONTEXT.md), [TODO.md](TODO.md) a
[DECISIONS.md](DECISIONS.md). Postup pro nový chat je v
[START_NEW_CODEX_CHAT.md](START_NEW_CODEX_CHAT.md).

## Zahájení relace

1. Přečti projektovou paměť a relevantní část `README.md`.
2. Spusť `git status --short --branch` a prohlédni existující diff. Necommitnuté
   změny považuj za práci uživatele, dokud není prokázán opak.
3. Ověř aktuální větev, poslední relevantní commity a stav úkolů v `TODO.md`.
4. Před významnou implementací stručně popiš plán a dotčené soubory.

## Bezpečnost a rozsah změn

- Zachovej současnou architekturu a rozhodnutí popsaná v `DECISIONS.md`.
- Neměň nesouvisející soubory a neopravuj vedlejší problémy bez zadání.
- Nepřepisuj ani nemaž existující uživatelské změny. Při překryvu nejprve
  zjisti jejich účel; pokud nelze bezpečně pokračovat, vyžádej si rozhodnutí.
- Bez výslovného souhlasu neprováděj commit, push, release ani deployment.
- Nevkládej do repozitáře API klíče, cookies, média, modely ani jiné citlivé či
  objemné runtime artefakty. `OPENAI_API_KEY` patří pouze do prostředí.
- Neprováděj skutečné placené API volání ani dlouhý přepis médií, pokud to úkol
  výslovně nevyžaduje.

## Implementace a architektura

- CLI orchestrace zůstává v `main.go`; znovupoužitelná lokální transkripční
  logika patří do `internal/localwhisper/`.
- Texty jazykových průchodů udržuj v `prompts/` a vkládej je do binárky přes
  `go:embed`. Neměň jejich význam jako vedlejší efekt jiné úpravy.
- `scripts/local_whisper_vad_subtitles.py` je referenční/paritní implementace,
  nikoli runtime závislost Go programu.
- Zdroj skillu v `skills/` a jeho zabalená kopie v `plugins/` musí při vědomé
  změně workflow zůstat obsahově synchronizované. ZIP artefakt aktualizuj jen
  tehdy, když to úkol výslovně vyžaduje.
- Zachovej obnovitelnost pipeline: hotové neprázdné mezivýstupy se přeskakují
  a Work úlohy se od běžných úloh odlišují markerem.
- Nové chování pokryj úzce zaměřenými testy; pro procesy a síť používej v
  jednotkových testech dočasné soubory a falešné executable soubory.

## Ověření a review

- Průběžně spouštěj nejmenší relevantní test a před předáním alespoň
  `go test ./...`.
- Podle rozsahu spusť také `go vet ./...`, `go test -race ./...` a
  `go build ./...`. End-to-end ověření externích programů uváděj samostatně;
  úspěch jednotkových testů ho nenahrazuje.
- Před dokončením spusť `git diff --check`, prohlédni `git diff` i
  `git diff --cached` a ověř, že změny odpovídají zadání.
- U změn pipeline, časování VTT, vypalování videa nebo práce s externími
  procesy doporuč nezávislé code review.

## Údržba projektové paměti

- Po významném úkolu aktualizuj pouze části paměti, které se skutečně změnily.
- Nové dlouhodobé technické rozhodnutí zapiš do `DECISIONS.md` s důvodem,
  stavem a datem, je-li spolehlivě známé.
- Rozpracované a plánované kroky udržuj v `TODO.md`; dokončené položky přesuň
  do stručné historie.
- Architekturu a aktuální schopnosti synchronizuj s `PROJECT_CONTEXT.md` a
  uživatelské použití s `README.md`. Nezapisuj domněnky jako fakta.

## Ukončení relace

Na závěr stručně uveď změněné soubory, provedené kontroly a jejich výsledky,
neověřené části nebo rizika a doporučený další krok. Projektovou paměť
aktualizuj před předáním, ale nezanášej do ní každou drobnou změnu kódu.

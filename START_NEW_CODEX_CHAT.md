# Jak začít nový Codex chat

## Doporučený postup

1. Otevři Codex v kořeni tohoto repozitáře.
2. Vlož níže uvedený úvodní prompt a doplň konkrétní nový úkol.
3. Nech Codex nejprve přečíst projektovou paměť a zkontrolovat Git. To je
   důležité zejména kvůli necommitnutým změnám, které mohou pocházet z
   předchozí relace.
4. Před commitem vždy ručně zkontroluj shrnutí, testy a diff. Codex nesmí bez
   výslovného souhlasu commitovat ani pushovat.

## Úvodní prompt

```text
Pokračuj v projektu vtt-cz z aktuálního pracovního stromu.

Nejprve přečti AGENTS.md, PROJECT_CONTEXT.md, TODO.md, DECISIONS.md,
START_NEW_CODEX_CHAT.md a relevantní část README.md. Potom spusť
git status --short --branch, prohlédni existující necommitnutý diff a ověř
poslední relevantní commity. Zachovej všechny uživatelské změny, současnou
architekturu a rozhodnutí; neměň nesouvisející soubory.

Můj úkol: <doplň konkrétní zadání>

Před významnou implementací stručně navrhni postup. Spusť relevantní testy,
na konci zkontroluj Git diff a podle skutečného výsledku aktualizuj projektovou
paměť. Bez mého výslovného souhlasu nedělej commit, push ani deployment.
```

## Kontrolní příkazy

Codex má na začátku použít zejména:

```bash
git status --short --branch
git diff --stat
git diff
git log --oneline --decorate -10
```

Před předáním změn má podle jejich rozsahu použít:

```bash
go test ./...
go vet ./...
go build ./...
git diff --check
git diff
git diff --cached
```

## Jak se paměť udržuje

Nejde o proces běžící na pozadí. Automatizace spočívá v pravidlech v
`AGENTS.md`: každá relace má při významném dokončeném úkolu aktualizovat
`PROJECT_CONTEXT.md`, `TODO.md` a případně `DECISIONS.md`, ale pouze podle
ověřeného stavu repozitáře. Git zůstává zdrojem detailní historie změn.

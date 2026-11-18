# VTT CZ Subtitle Workflow

Soukromy plugin pro ChatGPT Work, ktery navazuje na lokalni rezim programu
`vtt-cz --work`. Nepouziva zadnou externi aplikaci ani API programu.

## Co dela

Po nahrani jednoho souboru `*.en.raw.vtt` provede tri samostatne pruchody:

1. opravi zjevne chyby anglickeho prepisu a vytvori `*.en.vtt`,
2. prelozi titulky blok po bloku a vytvori `*.cs.raw.vtt`,
3. zkontroluje cestinu, upravi hranice vet a vytvori finalni `*.cs.vtt`.

Pri finalni kontrole je konec vety pevnou hranici titulku. Po tecce, otazniku
nebo vykricniku uz ve stejnem bloku nesmi zacinat dalsi veta. To plati i pro
velmi kratke vety. Pokud se rozdeleni bloku meni, plugin upravi dotcene casy
primerene delce mluveneho textu.

## Vytvoreni v ChatGPT Work

1. V ChatGPT Work otevri `@Plugin Creator`.
2. Vloz cely obsah souboru `PLUGIN-CREATOR-PROMPT.txt`.
3. Pokud Plugin Creator umozni nahrat hotovy balik, nahraj ZIP tohoto pluginu.
4. Zkontroluj, ze plugin obsahuje skill `vtt-cz-work-translation`, a uloz ho.

Plugin nepotrebuje MCP server, externi pripojeni ani prihlasovaci udaje.

## Pouziti

1. Spust `go run . --work ...` a pockej na lokalni prepis.
2. V ChatGPT Work vyber `@VTT CZ Subtitle Workflow`.
3. Nahraj cestu vypsanou programem jako `*.en.raw.vtt`.
4. Zadej: `Zpracuj tento soubor a vrat finalni cesky VTT pro vtt-cz.`
5. Stahni finalni `*.cs.vtt` do presne te cesty, kterou program vypsal.
6. Znovu spust `go run . --work`.

Plugin vraci take mezivysledky `*.en.vtt` a `*.cs.raw.vtt`; pro pokracovani
programu je rozhodujici pouze finalni `*.cs.vtt`.

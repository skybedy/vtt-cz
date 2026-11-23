# Technická rozhodnutí

Záznam shrnuje dlouhodobá rozhodnutí doložená kódem a historií. Datum je datum
odpovídajícího commitu, pokud je uvedeno.

## 2026-09-05 — Go CLI s externími multimediálními nástroji

**Stav:** přijato

Orchestrace je implementována jako Go CLI. Stahování a zpracování médií se
deleguje osvědčeným nástrojům jako `yt-dlp` a `ffmpeg`, místo aby se jejich
funkce znovu implementovaly. Výsledkem může být samostatná Go binárka, ale
externí nástroje zůstávají runtime požadavkem.

## 2026-09-06 — Izolované pracovní složky a obnovitelná pipeline

**Stav:** přijato

Každá úloha ukládá zdroj i mezivýstupy do vlastní časově pojmenované složky v
uživatelském adresáři videí. Jednotlivé kroky přeskakují již existující
neprázdný výstup. Důvodem je bezpečné pokračování po dlouhých nebo přerušených
operacích bez opakovaného stahování a placených volání.

## 2026-09-08 — Prompty jsou součástí binárky

**Stav:** přijato

Jazykové instrukce jsou oddělené v `prompts/`, ale do programu se vkládají
přes `go:embed`. Uživatel tak nemusí distribuovat zdrojový strom společně s
binárkou a prompty lze stále samostatně revidovat.

## 2026-11-18 — Dvě cesty zpracování jazyka

**Stav:** přijato

Vedle přímého OpenAI API workflow existuje `--work`: anglický přepis vznikne
lokálně, tři jazykové průchody provede ChatGPT Work plugin a CLI po návratu
finálního `.cs.vtt` naváže validací a vypálením. Marker `.work-mode` brání
záměně rozpracovaných úloh mezi oběma režimy.

## 2026-11-18 — Lokální transkripce běží nativně z Go

**Stav:** přijato

Produkční lokální pipeline v `internal/localwhisper/` používá whisper.cpp,
Silero VAD a DTW, ale její orchestrace a zpracování VTT jsou v Go. Python
skript zůstává pouze jako čitelná reference a paritní oracle; není runtime
závislostí hotové binárky. Výpočty jsou záměrně spouštěny s nízkou prioritou a
CLI nastavuje jeden Whisper thread, aby méně zatěžovalo počítač.

## 2026-11-18 — DVD sdílí zbytek titulkové pipeline

**Stav:** přijato

`HandBrakeCLI` slouží pouze k nalezení a převodu vybraného DVD titulu a audio
stopy. Po vzniku MP4 a MP3 pokračuje stejná transkripční, překladová a
vypalovací pipeline jako pro video stažené z URL.

## 2026-11-22 — Samostatný styl pro vertikální Reel video

**Stav:** přijato (`e3fe3ac`)

Přepínač `--reel` používá menší ASS styl titulků vhodný pro vertikální video.
Marker `.reel-mode` zachovává volbu při pokračování úlohy, včetně předání do
režimu ChatGPT Work.

## 2026-11-23 — Telegram jako experimentální VPS handoff

**Stav:** experimentální, čeká na reálné VPS ověření

Anglické a české VTT se mezi VPS a uživatelem předávají jako Telegram
dokumenty. Samostatný bot používá long polling, takže VPS nepotřebuje veřejný
HTTP endpoint. Hermes bot token se nesdílí, protože jeden bot má mít jedinou
přijímací integraci.

Příjem je omezen na nastavené kladné ID soukromého chatu i odesílatele. Český
VTT musí být odpovědí na původní zprávu konkrétní úlohy, mít koncovku `.vtt`,
velikost nejvýše 5 MB a projít vestavěnou WebVTT validací. Trvalým zdrojem
stavu zůstává VPS (`.telegram-handoff.json` a offset listeneru), nikoli chat.
Video se přes Telegram neposílá.

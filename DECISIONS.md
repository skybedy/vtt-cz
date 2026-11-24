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

## 2026-11-23 — E-mail jako první experimentální VPS handoff

**Stav:** experimentální, e-mailový VPS smoke test ověřen

Před nasazením Telegram varianty se anglické a české VTT předávají přes
vyhrazenou e-mailovou adresu. Vzdálená varianta používá SMTP s TLS nebo
STARTTLS a příjem IMAP přes TLS. Pro IMAP je přijata stabilní knihovna
`github.com/emersion/go-imap v1.2.1`, protože Go standardní knihovna IMAP
klienta neposkytuje.

Příchozí VTT musí být od přesně povolené adresy a obsahovat `In-Reply-To` nebo
`References` s náhodným `Message-ID` původní úlohy. Je povolena právě jedna
`.vtt` příloha do 5 MB, která musí projít WebVTT validací. Botova a uživatelská
adresa musí být různé, aby automatické stavové zprávy nevytvořily smyčku.
Heslo patří pouze do chráněného env souboru mimo repozitář.

Na VPS s vlastním Postfixem je preferovaný lokální transport: odchozí zprávy
předává `/usr/sbin/sendmail` a odpovědi čte bez změn z existujícího Maildiru.
Vzdálené SMTP/IMAP zůstává podporovanou alternativou. Lokální transport
nevyžaduje poštovní heslo a běží se stejnými oprávněními jako vlastník
Maildiru.

Chybějící runtime součásti se řeší explicitní read-only kontrolou `--doctor`,
která pro zvolený režim vypíše programy, modely a e-mailovou konfiguraci.
Aplikace sama nespouští privilegované instalátory ani nemění systém; tím se
odděluje opakovatelná diagnostika od distribučně specifického deploymentu.

Serverová konfigurace se může automaticky načíst z `/etc/vtt-cz/email.env`,
aby interaktivní CLI a systemd používaly stejné hodnoty. Procesní prostředí má
vyšší prioritu; soubor musí být běžný, nesmí být symlink ani být zapisovatelný
skupinou či ostatními uživateli.

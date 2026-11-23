# vtt-cz

Jednoduchy Go program, ktery stahne video, vytvori MP3, udela titulky a vypali
ceske titulky do noveho videa.

Program pocita s tim, ze mas v pocitaci nainstalovane:

- `yt-dlp`
- `ffmpeg` a `ffprobe`
- `HandBrakeCLI` z balicku `handbrake-cli`, pokud chces nacitat DVD
- Firefox, ze ktereho si `yt-dlp` ve vychozim nastaveni vezme cookies
- `OPENAI_API_KEY` v systemovem prostredi, pokud nepouzivas rezim `--work`

Go je potreba pouze pro sestaveni programu, ne pro spousteni hotove binarky.

## Sestaveni binarky

Pro tento Linuxovy notebook:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/vtt-cz .
```

Vysledny `bin/vtt-cz` je samostatna Go binarka. Obsahuje i textove prompty a
lokalni VAD/DTW logiku, takze za behu nepotrebuje zdrojovy repozitar, Go ani
Python. Externi programy a modely uvedene v teto dokumentaci zustavaji potreba.

Pro spousteni odkudkoli ji nainstaluj do uzivatelskeho `PATH`:

```bash
install -m 0755 bin/vtt-cz ~/.local/bin/vtt-cz
```

Potom staci v libovolnem adresari zadat `vtt-cz`.

## Kontrola zavislosti

Pred prvnim spustenim lze bez stahovani videa a bez API volani zkontrolovat
vsechny soucasti zvoleneho workflow:

```bash
vtt-cz --doctor --work --email
vtt-cz --doctor --email-listen
vtt-cz --doctor --work --dvd
```

Kontrola vypise nalezene programy, Whisper/VAD binarky a modely i stav
e-mailove konfigurace. Pri chybejici soucasti skonci nenulovym navratovym
kodem a uvede jeji cestu nebo doporuceny balicek. Sama nespousti `sudo` ani
neinstaluje systemove balicky.

Na serveru bez profilu Firefoxu nastav
`VTT_CZ_YTDLP_COOKIES_FROM_BROWSER=none`. Hodnota je jinak predvolene
`firefox` a muze obsahovat i profil podporovany `yt-dlp`. Pokud je `yt-dlp`
spravovan systemove, lze jeho pokus o vlastni aktualizaci vypnout pres
`VTT_CZ_YTDLP_AUTO_UPDATE=false`.

## Spusteni

```bash
./bin/vtt-cz
```

Program se zepta na URL:

```text
Zadej URL videa:
```

Tam muzes URL vlozit bez uvozovek.

Pokud program najde posledni rozpracovanou ulohu s jiz stazenym videem a MP3,
nabidne nejprve pokracovani z teto slozky. Po potvrzeni znovu nestahuje video,
nevytvari MP3 a preskoci take dalsi kroky, jejichz vystupni soubory uz existuji.

Kdyz chces i presto zacit nove video, odpovez `n` a potom zadej jeho URL.
URL zadana rovnou v prikazu vzdy zahaji novou ulohu.

URL lze zadat rovnou:

```bash
./bin/vtt-cz "https://www.youtube.com/watch?v=..."
```

## Vertikalni video / Reel

Pro vertikalni video ve formatu Reel pridej prepinac `--reel`. Titulky se
vypali mensim pismem, s mensim obrysem a odpovidajicim spodnim odsazenim, aby
na vysoke 9:16 plose nezabiraly neprimerene velkou cast obrazu:

```bash
./bin/vtt-cz --work --reel "https://www.facebook.com/reel/..."
```

Volbu lze kombinovat take s beznym cloudovym rezimem, `--local-whisper` nebo
`--dvd`. Program si ji v pracovni slozce zapamatuje i pro pozdejsi pokracovani.

## Lokalni Whisper

Pokud nechces posilat audio do placeneho OpenAI Whisper API, pridej prepinac
`--local-whisper`:

```bash
./bin/vtt-cz --local-whisper "https://www.youtube.com/watch?v=..."
```

Lokalni rezim pouziva odladeny prepis pres `whisper.cpp`, Silero VAD a DTW
casovani. Cela bezici pipeline je implementovana v Go. Puvodni Python skript
zustava v `scripts/local_whisper_vad_subtitles.py` pouze jako reference pro
porovnani implementaci; program jej nespousti a `python3` nepotrebuje.
Whisper bezi jen v jednom vlakne a se snizenou prioritou CPU i disku, aby
zatizoval pocitac co nejmene; prepis proto muze trvat vyrazne dele.
Ocekava tyto lokalni binarity a modely:

```text
~/whisper.cpp/build/bin/whisper-cli
~/whisper.cpp/build/bin/vad-speech-segments
~/whisper.cpp/models/ggml-large-v3.bin
~/whisper.cpp/models/for-tests-silero-v5.1.2-ggml.bin
```

Jine cesty lze nastavit promennymi `VTT_CZ_WHISPER_BIN`, `VTT_CZ_VAD_BIN`,
`VTT_CZ_WHISPER_MODEL` a `VTT_CZ_VAD_MODEL`. Lokalni Whisper nahrazuje pouze
prvni prepis audia. Oprava anglictiny, preklad a finalni kontrola cestiny
nadale pouzivaji GPT API, proto je i v tomto rezimu potreba `OPENAI_API_KEY`.

Prepinac lze kombinovat take s DVD:

```bash
./bin/vtt-cz --local-whisper --dvd
./bin/vtt-cz --local-whisper --dvd "/cesta/film.iso"
```

## Rezim ChatGPT Work bez API volani

Prepinac `--work` odstrani z programu jak cloudovy Whisper, tak vsechny
nasledne GPT API pozadavky. Prvni spusteni stahne video, prepise ho lokalnim
Whisperem a zastavi se:

```bash
./bin/vtt-cz --work "https://www.youtube.com/watch?v=..."
```

Program vypise cestu k `.en.raw.vtt` a presnou cestu, kam pozdeji ulozit
finalni `.cs.vtt`. Anglicky soubor nahraj do ChatGPT Work a vyber plugin
`@VTT CZ Subtitle Workflow`. Plugin postupne opravi anglictinu, prelozi
titulky do cestiny, udela finalni ceskou kontrolu a vrati tri VTT soubory.

Finalni soubor `.cs.vtt` uloz do cesty vypsane programem. Potom v adresari
programu znovu spust:

```bash
./bin/vtt-cz --work
```

Program najde posledni rozpracovanou Work ulohu, overi ceske VTT a vypali ho
do videa. Pro tento rezim neni potreba `OPENAI_API_KEY`. ChatGPT Work ale stale
cerpa vyuziti z limitu nebo kreditu prislusneho ChatGPT planu; pouze se
neuctuji API pozadavky tohoto programu.

Stejny rezim funguje s DVD:

```bash
./bin/vtt-cz --work --dvd
```

Hotovy plugin je v `plugins/vtt-cz-subtitle-workflow/`. Text, ktery lze
vlozit do `@Plugin Creator`, je v souboru
`plugins/vtt-cz-subtitle-workflow/PLUGIN-CREATOR-PROMPT.txt`. Samostatny zdroj
vnoreneho skillu zustava v `skills/vtt-cz-work-translation/`.

## Predani Work titulku e-mailem na VPS

Experimentalni e-mailovy rezim odstranuje rucni kopirovani VTT z VPS. Po
lokalnim prepisu posle aplikace `.en.raw.vtt` jako prilohu. Finalni `.cs.vtt`
posli jako odpoved na puvodni e-mail. Listener odpoved prevezme z lokalniho
Maildiru nebo pres IMAP, overi ji a automaticky spusti vypaleni titulku.

Botova adresa a povolena adresa uzivatele musi byt ruzne, aby se zabranilo
smycce automatickych odpovedi. K dispozici jsou dva transporty.

### Lokalni Postfix a Maildir

Pokud Postfix prijima odpovedi na stejne VPS, je preferovany transport
`local`. Nevyzaduje sitove prihlasovaci udaje: zpravy odesila pres kompatibilni
program `sendmail` a odpovedi pouze cte z adresaru `Maildir/new` a
`Maildir/cur`. Zpravy nepresouva ani neoznacuje jako prectene.

```bash
export VTT_CZ_EMAIL_TRANSPORT="local"
export VTT_CZ_EMAIL_FROM="vtt-cz@example.com"
export VTT_CZ_EMAIL_TO="uzivatel@example.net"
export VTT_CZ_EMAIL_ALLOWED_FROM="uzivatel@example.net"
export VTT_CZ_EMAIL_SENDMAIL="/usr/sbin/sendmail"
export VTT_CZ_EMAIL_MAILDIR="/home/uzivatel/Maildir"
export VTT_CZ_WORK_ROOT="/home/uzivatel/Videos/vtt-cz"
```

Postfix musi adresu z `VTT_CZ_EMAIL_FROM` dorucit do nastaveneho Maildiru.
Cursor pouziva cas posledni zpracovane zpravy a jeji relativni jmeno; pri
restartu proto navaze bez zmeny obsahu schranky.

### Vzdalene SMTP a IMAP

Pokud schranku hostuje jiny server, pouzij transport `smtp-imap`, samostatnou
schranku a aplikacni heslo. Heslo neposilej do repozitare.

Na VPS nastav:

```bash
export VTT_CZ_EMAIL_TRANSPORT="smtp-imap"
export VTT_CZ_EMAIL_SMTP_ADDR="smtp.example.com:465"
export VTT_CZ_EMAIL_SMTP_MODE="tls"
export VTT_CZ_EMAIL_IMAP_ADDR="imap.example.com:993"
export VTT_CZ_EMAIL_USERNAME="vtt-cz-bot@example.com"
export VTT_CZ_EMAIL_PASSWORD="aplikacni_heslo"
export VTT_CZ_EMAIL_FROM="vtt-cz-bot@example.com"
export VTT_CZ_EMAIL_TO="uzivatel@example.net"
export VTT_CZ_EMAIL_ALLOWED_FROM="uzivatel@example.net"
export VTT_CZ_EMAIL_MAILBOX="INBOX"
export VTT_CZ_WORK_ROOT="/home/uzivatel/Videos/vtt-cz"
```

Pro SMTP port 465 pouzij `VTT_CZ_EMAIL_SMTP_MODE=tls`. Pro port 587 se
STARTTLS pouzij `VTT_CZ_EMAIL_SMTP_MODE=starttls`. IMAP spojeni je vzdy
sifrovane TLS, obvykle na portu 993.

Listener nech bezet na pozadi:

```bash
./bin/vtt-cz --email-listen
```

Novou ulohu spust takto:

```bash
./bin/vtt-cz --work --email "https://www.youtube.com/watch?v=..."
```

Po prijeti anglickeho VTT:

1. stahni prilohu z e-mailu,
2. zpracuj ji pluginem `@VTT CZ Subtitle Workflow`,
3. odpovez na puvodni zpravu a priloz finalni `.cs.vtt`,
4. listener potvrdi validaci a po vypaleni oznami cestu k hotovemu videu.

Listener prijima jen zpravy z `VTT_CZ_EMAIL_ALLOWED_FROM` a vyzaduje vazbu
`In-Reply-To` nebo `References` na nahodne `Message-ID` puvodni ulohy. Prijme
prave jednu `.vtt` prilohu do 5 MB a spusti vestavenou WebVTT validaci. Stav
udrzuje v `.email-handoff.json`; pozici ve schrance uklada do
`.email-inbox-cursor.json` v korenove pracovni slozce.

Pro systemd user service lze pouzit sablonu
`deploy/vtt-cz-email.service.example`. Lokalni transport ma promenne v
`deploy/email-local.env.example`, vzdaleny v `deploy/email.env.example`.
Skutecny env soubor uloz mimo repozitar s pravy `0600`. Aby user service
bezela i po odhlaseni z VPS, muze byt potreba povolit systemd linger.

## DVD

Pro DVD vloz disk do mechaniky a spust:

```bash
./bin/vtt-cz --dvd
```

Program se pokusi mechaniku najit, prohleda DVD a vypise nalezene tituly s
jejich delkou. Enterem se vybere nejdelsi titul, ktery byva hlavnim filmem,
nebo muzes zadat jine cislo. Potom program nabidne nalezene zvukove stopy.
Vybrany titul prevede do MP4, vytvori z nej MP3 a pokracuje stejnym zpusobem
jako u videa stazeneho z URL.

Cestu ke zdroji muzes zadat rovnou. Podporovana je mechanika, ISO soubor i
slozka DVD, například:

```bash
./bin/vtt-cz --dvd /dev/sr0
./bin/vtt-cz --dvd "/cesta/film.iso"
./bin/vtt-cz --dvd "/cesta/VIDEO_TS"
```

Na Debianu nebo Ubuntu lze prikazovy HandBrake nainstalovat takto:

```bash
sudo apt install handbrake-cli
```

HandBrake umi nacist DVD, ale sam neobchazi ochranu proti kopirovani. U
chraneneho disku je nejprve potreba vytvorit vlastni nechranenou zalohu v
souladu s pravem platnym v miste pouziti a programu predat jeji ISO nebo
`VIDEO_TS`.

Program postupne:

1. vytvori novou pracovni slozku v systemove slozce videi,
2. pred stahovanim standardne zkontroluje aktualizaci `yt-dlp` pres `yt-dlp -U` a potom stahne video; pokud aktualizace kvuli siti nebo opravnenim selze, vypise varovani a zkusi pouzit stavajici verzi,
3. v rezimu `--dvd` misto toho prevede vybrany DVD titul do MP4,
4. vytvori MP3,
5. v beznem rezimu pripadne vytvori mensi MP3 pro limit OpenAI Whisper API,
6. posle audio na OpenAI Whisper, nebo ho s `--local-whisper` prepise lokalne pres `whisper.cpp`, a ulozi surove anglicke VTT titulky,
7. zkontroluje a opravi anglicke titulky pres GPT-5.5,
8. prelozi VTT titulky do cestiny pres GPT-5.5,
9. zkontroluje a uhladi ceske titulky pres GPT-5.5,
10. vypali finalni ceske titulky do noveho MP4 videa.

Pracovni slozka bude v:

```text
~/Videa/vtt-cz/
```

nebo v odpovidajici systemove slozce videi podle prikazu `xdg-user-dir VIDEOS`.
Na serveru lze koren prepsat promennou `VTT_CZ_WORK_ROOT`.

V pracovni slozce zustane:

- puvodni stazene video
- novy `.mp3` soubor
- pripadne mensi `.whisper.mp3` soubor pro OpenAI Whisper v cloudovem rezimu
- surovy anglicky `.en.raw.vtt` soubor
- opraveny anglicky `.en.vtt` soubor
- surovy cesky `.cs.raw.vtt` soubor
- finalni cesky `.cs.vtt` soubor
- nove `.cs.mp4` video s vypalenymi ceskymi titulky

Prompty pro kontrolu a preklad jsou v adresari `prompts/`.

Video s titulky se koduje pres `libx264` s `crf 20`.
Nizsi CRF znamena vyssi kvalitu a vetsi soubor.

Po nahrani hotoveho videa muzes celou pracovni slozku smazat.

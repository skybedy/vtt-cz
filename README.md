# vtt-cz

Jednoduchy Go program, ktery stahne video, vytvori MP3, udela titulky a vypali
ceske titulky do noveho videa.

Program pocita s tim, ze mas v pocitaci nainstalovane:

- Go
- `yt-dlp`
- `ffmpeg`
- Firefox, ze ktereho si `yt-dlp` vezme cookies
- `OPENAI_API_KEY` v systemovem prostredi

## Spusteni

```bash
go run .
```

Program se zepta na URL:

```text
Zadej URL videa:
```

Tam muzes URL vlozit bez uvozovek.

Jde to spustit i puvodnim zpusobem:

```bash
go run . "https://www.youtube.com/watch?v=..."
```

Program postupne:

1. vytvori novou pracovni slozku v systemove slozce videi,
2. spusti `yt-dlp`, ktery stahne nejlepsi dostupny videosoubor s obrazem i zvukem,
3. vytvori MP3,
4. posle MP3 na OpenAI Whisper a ulozi surove anglicke VTT titulky,
5. zkontroluje a opravi anglicke titulky pres GPT-5.5,
6. prelozi VTT titulky do cestiny pres GPT-5.5,
7. zkontroluje a uhladi ceske titulky pres GPT-5.5,
8. vypali finalni ceske titulky do noveho MP4 videa.

Pracovni slozka bude v:

```text
~/Videa/vtt-cz/
```

nebo v odpovidajici systemove slozce videi podle prikazu `xdg-user-dir VIDEOS`.

V pracovni slozce zustane:

- puvodni stazene video
- novy `.mp3` soubor
- surovy anglicky `.en.raw.vtt` soubor
- opraveny anglicky `.en.vtt` soubor
- surovy cesky `.cs.raw.vtt` soubor
- finalni cesky `.cs.vtt` soubor
- nove `.cs.mp4` video s vypalenymi ceskymi titulky

Video s titulky se koduje pres `libx264` s `crf 20`.
Nizsi CRF znamena vyssi kvalitu a vetsi soubor.

Po nahrani hotoveho videa muzes celou pracovni slozku smazat.

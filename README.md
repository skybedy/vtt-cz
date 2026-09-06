# yt-dlp-audio

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

1. spusti `yt-dlp`, ktery stahne nejlepsi dostupny videosoubor s obrazem i zvukem,
2. vytvori MP3,
3. posle MP3 na OpenAI Whisper a ulozi anglicke VTT titulky,
4. prelozi VTT titulky do cestiny pres GPT-5,
5. vypali ceske titulky do noveho MP4 videa.

V aktualnim adresari zustane:

- puvodni stazene video
- novy `.mp3` soubor
- anglicky `.en.vtt` soubor
- cesky `.cs.vtt` soubor
- nove `.cs.mp4` video s vypalenymi ceskymi titulky

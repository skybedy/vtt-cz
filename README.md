# yt-dlp-wav

Jednoduchy Go program, ktery stahne video pomoci `yt-dlp` a prevede ho na WAV.

Program pocita s tim, ze mas v pocitaci nainstalovane:

- Go
- `yt-dlp`
- `ffmpeg`

## Spusteni

```bash
go run . "URL_VIDEA"
```

Priklad:

```bash
go run . "https://www.youtube.com/watch?v=..."
```

Vysledny `.wav` soubor se ulozi do aktualniho adresare.

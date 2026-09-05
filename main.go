package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Pouziti: go run . \"URL_VIDEA\"")
		os.Exit(1)
	}

	url := os.Args[1]
	cmd := exec.Command(
		"yt-dlp",
		"--extract-audio",
		"--audio-format",
		"wav",
		url,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba:", err)
		os.Exit(1)
	}
}

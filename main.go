package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	openAITranscriptionsURL = "https://api.openai.com/v1/audio/transcriptions"
	openAIResponsesURL      = "https://api.openai.com/v1/responses"
	maxTranscriptionBytes   = 25 * 1024 * 1024
)

func main() {
	url := readURL()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Chybi promenna prostredi OPENAI_API_KEY.")
		os.Exit(1)
	}

	workDir, err := createWorkDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri vytvareni pracovni slozky:", err)
		os.Exit(1)
	}
	fmt.Println("Pracovni slozka:", workDir)

	videoPath, mp3Path, err := downloadVideoAndMP3(url, workDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Chyba:", err)
		os.Exit(1)
	}

	enVTTPath := replaceExtension(videoPath, ".en.vtt")
	csVTTPath := replaceExtension(videoPath, ".cs.vtt")
	outputVideoPath := replaceExtension(videoPath, ".cs.mp4")

	if err := createEnglishVTT(apiKey, mp3Path, enVTTPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri vytvareni anglickych titulku:", err)
		os.Exit(1)
	}

	if err := translateVTTToCzech(apiKey, enVTTPath, csVTTPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri prekladu titulku:", err)
		os.Exit(1)
	}

	if err := burnSubtitles(videoPath, csVTTPath, outputVideoPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri vypalovani titulku:", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Hotovo:")
	fmt.Println("Pracovni slozka:", workDir)
	fmt.Println("Video:", videoPath)
	fmt.Println("Audio:", mp3Path)
	fmt.Println("Anglicke titulky:", enVTTPath)
	fmt.Println("Ceske titulky:", csVTTPath)
	fmt.Println("Video s ceskymi titulky:", outputVideoPath)
}

func readURL() string {
	url := ""

	if len(os.Args) == 1 {
		fmt.Print("Zadej URL videa: ")

		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			url = scanner.Text()
		}
	} else if len(os.Args) == 2 {
		if os.Args[1] == "-h" || os.Args[1] == "--help" {
			printUsage()
			os.Exit(0)
		}

		url = os.Args[1]
	} else {
		printUsage()
		os.Exit(1)
	}

	url = strings.TrimSpace(url)
	url = strings.TrimRight(url, "\\")
	url = cleanURL(url)
	if url == "" {
		fmt.Println("Nebyla zadana zadna URL.")
		os.Exit(1)
	}

	return url
}

func createWorkDir() (string, error) {
	videosDir := userVideosDir()
	workDir := filepath.Join(videosDir, "vtt-cz", time.Now().Format("20060102-150405"))

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}

	return workDir, nil
}

func userVideosDir() string {
	cmd := exec.Command("xdg-user-dir", "VIDEOS")
	output, err := cmd.Output()
	if err == nil {
		videosDir := strings.TrimSpace(string(output))
		if videosDir != "" {
			return videosDir
		}
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "."
	}

	return filepath.Join(homeDir, "Videos")
}

func downloadVideoAndMP3(url string, workDir string) (string, string, error) {
	pathFile, err := os.CreateTemp("", "vtt-cz-path-*.txt")
	if err != nil {
		return "", "", err
	}
	pathFilePath := pathFile.Name()
	pathFile.Close()
	defer os.Remove(pathFilePath)

	cmd := exec.Command(
		"yt-dlp",
		"--cookies-from-browser",
		"firefox",
		"--format",
		"best[acodec!=none][vcodec!=none]/best",
		"--extract-audio",
		"--audio-format",
		"mp3",
		"--audio-quality",
		"64K",
		"--keep-video",
		"--print-to-file",
		"after_move:filepath",
		pathFilePath,
		url,
	)

	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", "", err
	}

	pathFileBytes, err := os.ReadFile(pathFilePath)
	if err != nil {
		return "", "", err
	}

	videoPath := lastLine(string(pathFileBytes))
	if videoPath == "" {
		return "", "", fmt.Errorf("yt-dlp nevratil cestu ke stazenemu videu")
	}
	if !filepath.IsAbs(videoPath) {
		videoPath = filepath.Join(workDir, videoPath)
	}

	mp3Path := replaceExtension(videoPath, ".mp3")
	if _, err := os.Stat(mp3Path); err != nil {
		return "", "", fmt.Errorf("nenasel jsem vytvoreny MP3 soubor %q", mp3Path)
	}

	return videoPath, mp3Path, nil
}

func createEnglishVTT(apiKey string, mp3Path string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vytvarim anglicke VTT titulky pres Whisper...")

	mp3Info, err := os.Stat(mp3Path)
	if err != nil {
		return err
	}
	if mp3Info.Size() > maxTranscriptionBytes {
		return fmt.Errorf("MP3 soubor ma %.1f MB, ale OpenAI transcription API prijima nejvyse 25 MB", float64(mp3Info.Size())/1024/1024)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	file, err := os.Open(mp3Path)
	if err != nil {
		return err
	}
	defer file.Close()

	fileWriter, err := writer.CreateFormFile("file", filepath.Base(mp3Path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fileWriter, file); err != nil {
		return err
	}

	writer.WriteField("model", "whisper-1")
	writer.WriteField("response_format", "vtt")
	writer.Close()

	req, err := http.NewRequest(http.MethodPost, openAITranscriptionsURL, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("OpenAI API vratilo %s: %s", resp.Status, string(responseBody))
	}

	return os.WriteFile(outputPath, responseBody, 0o644)
}

func translateVTTToCzech(apiKey string, inputPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Prekladam titulky do cestiny pres GPT-5...")

	vttBytes, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	requestBody := map[string]any{
		"model": "gpt-5",
		"input": []map[string]any{
			{
				"role": "system",
				"content": []map[string]string{
					{
						"type": "input_text",
						"text": czechSubtitlePrompt(),
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]string{
					{
						"type": "input_text",
						"text": string(vttBytes),
					},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, openAIResponsesURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("OpenAI API vratilo %s: %s", resp.Status, string(responseBody))
	}

	translatedVTT, err := responseOutputText(responseBody)
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, []byte(translatedVTT), 0o644)
}

func burnSubtitles(videoPath string, subtitlesPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vypaluji ceske titulky do videa...")

	filter := fmt.Sprintf(
		"subtitles=%s:force_style='FontSize=16,PrimaryColour=&H00FFFFFF,OutlineColour=&H40000000,BackColour=&H80000000,BorderStyle=3,Outline=4,Shadow=0,Alignment=2,MarginV=55'",
		escapeSubtitlePath(subtitlesPath),
	)

	cmd := exec.Command(
		"nice",
		"-n",
		"19",
		"ionice",
		"-c3",
		"ffmpeg",
		"-y",
		"-threads",
		"1",
		"-i",
		videoPath,
		"-vf",
		filter,
		"-c:v",
		"libx264",
		"-preset",
		"veryfast",
		"-crf",
		"23",
		"-c:a",
		"copy",
		outputPath,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func printUsage() {
	fmt.Println("Pouziti:")
	fmt.Println("  go run .")
	fmt.Println("  go run . \"URL_VIDEA\"")
}

func cleanURL(input string) string {
	if strings.HasPrefix(input, "[") {
		start := strings.Index(input, "](")
		end := strings.LastIndex(input, ")")

		if start != -1 && end > start+2 {
			return strings.TrimSpace(input[start+2 : end])
		}
	}

	return input
}

func czechSubtitlePrompt() string {
	return `Preloz prilozeny soubor titulku VTT do prirozene cestiny.

Vrat pouze obsah vysledneho ceskeho VTT souboru. Nepridavej komentare, vysvetleni ani Markdown.

Zachovej presne:
- puvodni format VTT,
- vsechny casove znacky,
- pocet bloku,
- poradi bloku,
- prazdne radky mezi bloky.

Prekladej blok po bloku. Kazdy cesky blok musi vyznamove odpovidat pouze textu ve stejnem puvodnim casovem bloku.

Nepresouvej vyznam mezi bloky, neslucuj bloky, nerozdeluj bloky, nevynechavej bloky a neprekladej soubor jako souvisly text.

Pred vracenim vysledku zkontroluj:
1. pocet bloku,
2. shodu casovych znacek,
3. prvni blok,
4. poslednich 10 az 15 bloku,
5. skutecnou posledni vetu originalu.

Zvlast hlidej, aby posledni ceske bloky odpovidaly skutecnemu konci originalu, ne pasazi, ktera patri drive.

Pokud je z kontextu jasne pohlavi mluvciho, respektuj ho v cestine. Pokud jasne neni, pouzivej neutralni formulace.`
}

func responseOutputText(responseBody []byte) (string, error) {
	var response struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}

	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", err
	}

	if strings.TrimSpace(response.OutputText) != "" {
		return strings.TrimSpace(response.OutputText), nil
	}

	var builder strings.Builder
	for _, output := range response.Output {
		for _, content := range output.Content {
			builder.WriteString(content.Text)
		}
	}

	text := strings.TrimSpace(builder.String())
	if text == "" {
		return "", fmt.Errorf("odpoved OpenAI neobsahovala text")
	}

	return text, nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return ""
	}

	return strings.TrimSpace(lines[len(lines)-1])
}

func replaceExtension(path string, newExtension string) string {
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + newExtension
}

func escapeSubtitlePath(path string) string {
	return strings.NewReplacer(
		"\\", "\\\\",
		":", "\\:",
		"'", "\\'",
		",", "\\,",
	).Replace(path)
}

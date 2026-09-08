package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	openAITranscriptionsURL = "https://api.openai.com/v1/audio/transcriptions"
	openAIResponsesURL      = "https://api.openai.com/v1/responses"
	maxTranscriptionBytes   = 25 * 1024 * 1024
)

//go:embed prompts/czech_translation.txt
var czechTranslationPrompt string

//go:embed prompts/english_revision.txt
var englishRevisionPrompt string

//go:embed prompts/czech_revision.txt
var czechRevisionPrompt string

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

	rawEnVTTPath := replaceExtension(videoPath, ".en.raw.vtt")
	enVTTPath := replaceExtension(videoPath, ".en.vtt")
	rawCsVTTPath := replaceExtension(videoPath, ".cs.raw.vtt")
	csVTTPath := replaceExtension(videoPath, ".cs.vtt")
	outputVideoPath := replaceExtension(videoPath, ".cs.mp4")

	if err := createEnglishVTT(apiKey, mp3Path, rawEnVTTPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri vytvareni anglickych titulku:", err)
		os.Exit(1)
	}

	if err := reviseEnglishVTT(apiKey, rawEnVTTPath, enVTTPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri kontrole anglickych titulku:", err)
		os.Exit(1)
	}

	if err := translateVTTToCzech(apiKey, enVTTPath, rawCsVTTPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri prekladu titulku:", err)
		os.Exit(1)
	}

	if err := reviseCzechVTT(apiKey, rawCsVTTPath, csVTTPath); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri kontrole ceskych titulku:", err)
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
	fmt.Println("Anglicke titulky z Whisperu:", rawEnVTTPath)
	fmt.Println("Opravene anglicke titulky:", enVTTPath)
	fmt.Println("Ceske titulky po prekladu:", rawCsVTTPath)
	fmt.Println("Finalni ceske titulky:", csVTTPath)
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
		url,
	)

	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", "", err
	}

	videoPath, err := findFileByExtensions(workDir, []string{".mp4", ".webm", ".mkv", ".mov"})
	if err != nil {
		return "", "", err
	}

	mp3Path, err := findFileByExtensions(workDir, []string{".mp3"})
	if err != nil {
		return "", "", err
	}

	return videoPath, mp3Path, nil
}

func createEnglishVTT(apiKey string, mp3Path string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vytvarim anglicke VTT titulky pres Whisper...")

	transcriptionAudioPath, err := prepareTranscriptionAudio(mp3Path)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	file, err := os.Open(transcriptionAudioPath)
	if err != nil {
		return err
	}
	defer file.Close()

	fileWriter, err := writer.CreateFormFile("file", filepath.Base(transcriptionAudioPath))
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

func prepareTranscriptionAudio(mp3Path string) (string, error) {
	mp3Info, err := os.Stat(mp3Path)
	if err != nil {
		return "", err
	}

	if mp3Info.Size() <= maxTranscriptionBytes {
		return mp3Path, nil
	}

	fmt.Printf(
		"MP3 soubor ma %.1f MB, vytvarim mensi kopii pro Whisper...\n",
		float64(mp3Info.Size())/1024/1024,
	)

	outputPath := replaceExtension(mp3Path, ".whisper.mp3")
	bitrates := []string{"32k", "24k", "16k"}

	for _, bitrate := range bitrates {
		fmt.Println("Zkousim audio pro Whisper s bitrate:", bitrate)

		cmd := exec.Command(
			"ffmpeg",
			"-y",
			"-i",
			mp3Path,
			"-vn",
			"-ac",
			"1",
			"-ar",
			"16000",
			"-b:a",
			bitrate,
			outputPath,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return "", err
		}

		outputInfo, err := os.Stat(outputPath)
		if err != nil {
			return "", err
		}

		if outputInfo.Size() <= maxTranscriptionBytes {
			fmt.Printf(
				"Audio pro Whisper ma %.1f MB: %s\n",
				float64(outputInfo.Size())/1024/1024,
				outputPath,
			)
			return outputPath, nil
		}
	}

	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return "", err
	}

	return "", fmt.Errorf(
		"audio pro Whisper ma i po zmenseni %.1f MB, ale OpenAI transcription API prijima nejvyse 25 MB",
		float64(outputInfo.Size())/1024/1024,
	)
}

func translateVTTToCzech(apiKey string, inputPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Prekladam titulky do cestiny pres GPT-5.5...")

	vttBytes, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	translatedVTT, err := askGPT(apiKey, vttBytes, czechTranslationPrompt)
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, []byte(translatedVTT), 0o644)
}

func reviseEnglishVTT(apiKey string, inputPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Kontroluji anglicke titulky pres GPT-5.5...")

	vttBytes, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	revisedVTT, err := askGPT(apiKey, vttBytes, englishRevisionPrompt)
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, []byte(revisedVTT), 0o644)
}

func reviseCzechVTT(apiKey string, inputPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Kontroluji ceske titulky pres GPT-5.5...")

	vttBytes, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}

	revisedVTT, err := askGPT(apiKey, vttBytes, czechRevisionPrompt)
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, []byte(revisedVTT), 0o644)
}

func askGPT(apiKey string, input []byte, prompt string) (string, error) {
	requestBody := map[string]any{
		"model": "gpt-5.5",
		"input": []map[string]any{
			{
				"role": "system",
				"content": []map[string]string{
					{
						"type": "input_text",
						"text": prompt,
					},
				},
			},
			{
				"role": "user",
				"content": []map[string]string{
					{
						"type": "input_text",
						"text": string(input),
					},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, openAIResponsesURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("OpenAI API vratilo %s: %s", resp.Status, string(responseBody))
	}

	return responseOutputText(responseBody)
}

func burnSubtitles(videoPath string, subtitlesPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vypaluji ceske titulky do videa...")

	style := "FontSize=16,PrimaryColour=&H00FFFFFF,OutlineColour=&H40000000,BackColour=&H80000000,BorderStyle=3,Outline=4,Shadow=0,Alignment=2,MarginV=50"
	filter := fmt.Sprintf(
		"subtitles=filename=%s:force_style=%s",
		quoteFilterValue(subtitlesPath),
		quoteFilterValue(style),
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
		"20",
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

func replaceExtension(path string, newExtension string) string {
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + newExtension
}

func quoteFilterValue(value string) string {
	escaped := strings.NewReplacer(
		"\\", "\\\\",
		"'", "\\'",
	).Replace(value)

	return "'" + escaped + "'"
}

func findFileByExtensions(dir string, extensions []string) (string, error) {
	var matches []string

	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		fileExtension := strings.ToLower(filepath.Ext(path))
		for _, extension := range extensions {
			if fileExtension == extension {
				matches = append(matches, path)
				break
			}
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Strings(matches)
	if len(matches) == 0 {
		return "", fmt.Errorf("ve slozce %q jsem nenasel soubor s priponou %s", dir, strings.Join(extensions, ", "))
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ve slozce %q jsem nasel vic souboru s priponou %s", dir, strings.Join(extensions, ", "))
	}

	return matches[0], nil
}

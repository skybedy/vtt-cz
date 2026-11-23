package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vtt-cz/internal/localwhisper"
)

const (
	openAITranscriptionsURL = "https://api.openai.com/v1/audio/transcriptions"
	openAIResponsesURL      = "https://api.openai.com/v1/responses"
	maxTranscriptionBytes   = 25 * 1024 * 1024
	standardSubtitleStyle   = "FontSize=16,PrimaryColour=&H00FFFFFF,OutlineColour=&H40000000,BackColour=&H80000000,BorderStyle=3,Outline=4,Shadow=0,Alignment=2,MarginV=50"
	reelSubtitleStyle       = "FontSize=9,PrimaryColour=&H00FFFFFF,OutlineColour=&H40000000,BackColour=&H80000000,BorderStyle=3,Outline=2,Shadow=0,Alignment=2,MarginV=28"
)

//go:embed prompts/czech_translation.txt
var czechTranslationPrompt string

//go:embed prompts/english_revision.txt
var englishRevisionPrompt string

//go:embed prompts/czech_revision.txt
var czechRevisionPrompt string

type cliOptions struct {
	mode          string
	input         string
	localWhisper  bool
	workMode      bool
	reelMode      bool
	emailMode     bool
	emailListener bool
}

func main() {
	if wantsHelp(os.Args[1:]) {
		printUsage()
		return
	}

	options, err := parseArguments(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "Chyba:", err)
		printUsage()
		os.Exit(1)
	}
	if options.emailListener {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runEmailListenerFromEnvironment(ctx, workRootDir()); err != nil {
			fmt.Fprintln(os.Stderr, "Chyba e-mailoveho listeneru:", err)
			os.Exit(1)
		}
		return
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if !options.workMode && apiKey == "" {
		fmt.Fprintln(os.Stderr, "Chybi promenna prostredi OPENAI_API_KEY.")
		os.Exit(1)
	}

	reader := bufio.NewReader(os.Stdin)
	workDir, videoPath, mp3Path, resumed, err := resumeExistingWork(reader, options.mode, options.input, options.workMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri hledani rozpracovane ulohy:", err)
		os.Exit(1)
	}

	if !resumed {
		workDir, err = createWorkDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Chyba pri vytvareni pracovni slozky:", err)
			os.Exit(1)
		}
		fmt.Println("Pracovni slozka:", workDir)
		if options.workMode {
			if err := os.WriteFile(filepath.Join(workDir, ".work-mode"), []byte("work\n"), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "Chyba pri oznaceni Work ulohy:", err)
				os.Exit(1)
			}
		}
		switch options.mode {
		case "dvd":
			videoPath, mp3Path, err = importDVD(reader, options.input, workDir)
		default:
			url := readURL(reader, options.input)
			videoPath, mp3Path, err = downloadVideoAndMP3(url, workDir)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "Chyba:", err)
			os.Exit(1)
		}
	}
	reelMarkerPath := filepath.Join(workDir, ".reel-mode")
	if options.reelMode && !fileExists(reelMarkerPath) {
		if err := os.WriteFile(reelMarkerPath, []byte("reel\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "Chyba pri oznaceni Reel ulohy:", err)
			os.Exit(1)
		}
	}
	options.reelMode = options.reelMode || fileExists(reelMarkerPath)
	emailMarkerPath := filepath.Join(workDir, ".email-mode")
	if options.emailMode && !fileExists(emailMarkerPath) {
		if err := os.WriteFile(emailMarkerPath, []byte("email\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "Chyba pri oznaceni e-mailove ulohy:", err)
			os.Exit(1)
		}
	}
	options.emailMode = options.emailMode || fileExists(emailMarkerPath)

	rawEnVTTPath := replaceExtension(videoPath, ".en.raw.vtt")
	enVTTPath := replaceExtension(videoPath, ".en.vtt")
	rawCsVTTPath := replaceExtension(videoPath, ".cs.raw.vtt")
	csVTTPath := replaceExtension(videoPath, ".cs.vtt")
	outputVideoPath := replaceExtension(videoPath, ".cs.mp4")
	if options.workMode {
		if err := runWorkMode(workDir, videoPath, mp3Path, rawEnVTTPath, csVTTPath, outputVideoPath, options.reelMode, options.emailMode); err != nil {
			fmt.Fprintln(os.Stderr, "Chyba v rezimu ChatGPT Work:", err)
			os.Exit(1)
		}
		return
	}

	if err := runUnlessFileExists(rawEnVTTPath, "Anglicke titulky z Whisperu uz existuji, preskakuji tento krok.", func() error {
		return createEnglishVTT(apiKey, mp3Path, rawEnVTTPath, options.localWhisper)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri vytvareni anglickych titulku:", err)
		os.Exit(1)
	}

	if err := runUnlessFileExists(enVTTPath, "Opravene anglicke titulky uz existuji, preskakuji tento krok.", func() error {
		return reviseEnglishVTT(apiKey, rawEnVTTPath, enVTTPath)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri kontrole anglickych titulku:", err)
		os.Exit(1)
	}

	if err := runUnlessFileExists(rawCsVTTPath, "Ceske titulky po prekladu uz existuji, preskakuji tento krok.", func() error {
		return translateVTTToCzech(apiKey, enVTTPath, rawCsVTTPath)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri prekladu titulku:", err)
		os.Exit(1)
	}

	if err := runUnlessFileExists(csVTTPath, "Finalni ceske titulky uz existuji, preskakuji tento krok.", func() error {
		return reviseCzechVTT(apiKey, rawCsVTTPath, csVTTPath)
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Chyba pri kontrole ceskych titulku:", err)
		os.Exit(1)
	}

	if err := runUnlessFileExists(outputVideoPath, "Video s ceskymi titulky uz existuje, preskakuji tento krok.", func() error {
		return burnSubtitles(videoPath, csVTTPath, outputVideoPath, options.reelMode)
	}); err != nil {
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

func wantsHelp(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help")
}

func parseArguments(args []string) (cliOptions, error) {
	options := cliOptions{mode: "url"}

	for _, arg := range args {
		switch arg {
		case "--dvd":
			if options.mode == "dvd" {
				return cliOptions{}, fmt.Errorf("prepinac --dvd je zadan vicekrat")
			}
			options.mode = "dvd"
		case "--local-whisper":
			if options.localWhisper {
				return cliOptions{}, fmt.Errorf("prepinac --local-whisper je zadan vicekrat")
			}
			options.localWhisper = true
		case "--work":
			if options.workMode {
				return cliOptions{}, fmt.Errorf("prepinac --work je zadan vicekrat")
			}
			options.workMode = true
		case "--reel":
			if options.reelMode {
				return cliOptions{}, fmt.Errorf("prepinac --reel je zadan vicekrat")
			}
			options.reelMode = true
		case "--email":
			if options.emailMode {
				return cliOptions{}, fmt.Errorf("prepinac --email je zadan vicekrat")
			}
			options.emailMode = true
		case "--email-listen":
			if options.emailListener {
				return cliOptions{}, fmt.Errorf("prepinac --email-listen je zadan vicekrat")
			}
			options.emailListener = true
		default:
			if strings.HasPrefix(arg, "-") {
				return cliOptions{}, fmt.Errorf("neznamy prepinac %q", arg)
			}
			if options.input != "" {
				return cliOptions{}, fmt.Errorf("lze zadat nejvyse jednu URL nebo cestu k DVD")
			}
			options.input = arg
		}
	}
	if options.localWhisper && options.workMode {
		return cliOptions{}, fmt.Errorf("--work uz lokalni Whisper zapina; nepouzivej soucasne --local-whisper")
	}
	if options.emailMode && !options.workMode {
		return cliOptions{}, fmt.Errorf("--email lze pouzit pouze spolu s --work")
	}
	if options.emailListener && (options.mode != "url" || options.input != "" || options.localWhisper || options.workMode || options.reelMode || options.emailMode) {
		return cliOptions{}, fmt.Errorf("--email-listen nelze kombinovat s jinymi prepinaci ani vstupem")
	}

	return options, nil
}

func readURL(reader *bufio.Reader, initialURL string) string {
	url := initialURL

	if url == "" {
		fmt.Print("Zadej URL videa: ")
		line, err := reader.ReadString('\n')
		if err == nil || len(line) > 0 {
			url = line
		}
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

func resumeExistingWork(reader *bufio.Reader, mode string, input string, workMode bool) (string, string, string, bool, error) {
	if mode != "url" || input != "" {
		return "", "", "", false, nil
	}

	workDir, videoPath, mp3Path, found, err := findLatestIncompleteWork(workMode)
	if err != nil || !found {
		return "", "", "", false, err
	}

	fmt.Println("Nalezena rozpracovana uloha:", workDir)
	fmt.Println("Video:", videoPath)
	fmt.Println("Audio:", mp3Path)
	fmt.Print("Chces z ni pokracovat? [A/n]: ")

	answer, readErr := reader.ReadString('\n')
	if readErr != nil && len(answer) == 0 && readErr != io.EOF {
		return "", "", "", false, readErr
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer == "" || answer == "a" || answer == "ano" || answer == "y" || answer == "yes" {
		fmt.Println("Pokracuji bez opetovneho stahovani videa a vytvareni MP3.")
		return workDir, videoPath, mp3Path, true, nil
	}

	return "", "", "", false, nil
}

func findLatestIncompleteWork(workMode bool) (string, string, string, bool, error) {
	rootDir := workRootDir()
	entries, err := os.ReadDir(rootDir)
	if os.IsNotExist(err) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		workDir := filepath.Join(rootDir, entry.Name())
		if fileExists(filepath.Join(workDir, ".work-mode")) != workMode {
			continue
		}
		videoPath, mp3Path, found, err := findExistingVideoAndMP3(workDir)
		if err != nil {
			return "", "", "", false, err
		}
		if !found || fileExists(replaceExtension(videoPath, ".cs.mp4")) {
			continue
		}

		return workDir, videoPath, mp3Path, true, nil
	}

	return "", "", "", false, nil
}

func findExistingVideoAndMP3(workDir string) (string, string, bool, error) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return "", "", false, err
	}

	videoExtensions := map[string]bool{
		".mp4":  true,
		".webm": true,
		".mkv":  true,
		".mov":  true,
	}
	videosByStem := make(map[string]string)
	var mp3Paths []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		extension := strings.ToLower(filepath.Ext(name))
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		path := filepath.Join(workDir, name)

		if extension == ".mp3" && !strings.HasSuffix(strings.ToLower(stem), ".whisper") && fileExists(path) {
			mp3Paths = append(mp3Paths, path)
		}
		if videoExtensions[extension] && !strings.HasSuffix(strings.ToLower(stem), ".cs") && fileExists(path) {
			videosByStem[stem] = path
		}
	}

	sort.Strings(mp3Paths)
	for _, mp3Path := range mp3Paths {
		stem := strings.TrimSuffix(filepath.Base(mp3Path), filepath.Ext(mp3Path))
		if videoPath, ok := videosByStem[stem]; ok {
			return videoPath, mp3Path, true, nil
		}
	}

	return "", "", false, nil
}

func runUnlessFileExists(path string, skipMessage string, run func() error) error {
	if fileExists(path) {
		fmt.Println()
		fmt.Println(skipMessage)
		return nil
	}

	return run()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func runWorkMode(workDir string, videoPath string, mp3Path string, rawEnVTTPath string, csVTTPath string, outputVideoPath string, reelMode bool, emailMode bool) error {
	if err := runUnlessFileExists(rawEnVTTPath, "Lokalni anglicke titulky uz existuji, preskakuji prepis.", func() error {
		return createEnglishVTTLocal(mp3Path, rawEnVTTPath)
	}); err != nil {
		return fmt.Errorf("lokalni vytvoreni anglickych titulku: %w", err)
	}

	if !fileExists(csVTTPath) {
		instructionsPath := filepath.Join(workDir, "CHATGPT-WORK.txt")
		resumeArguments := []string{"--work"}
		if reelMode {
			resumeArguments = append(resumeArguments, "--reel")
		}
		resumeCommand := executableCommand(resumeArguments...)
		var instructions string
		if emailMode {
			instructions = fmt.Sprintf(
				"CHATGPT WORK - DOKONCENI TITULKU PRES E-MAIL\n\n"+
					"1. Stahni anglicke VTT z prilohy e-mailu.\n"+
					"2. V ChatGPT Work vyber plugin @VTT CZ Subtitle Workflow.\n"+
					"3. Nahraj VTT a zadej: Zpracuj tento soubor a vrat finalni cesky VTT pro vtt-cz.\n"+
					"4. Odpovez na puvodni e-mail a priloz finalni .cs.vtt.\n\n"+
					"Zdrojovy soubor na VPS: %s\n"+
					"Cilovy soubor na VPS: %s\n\n"+
					"E-mailovy listener soubor overi a vypali titulky do videa.\n",
				rawEnVTTPath,
				csVTTPath,
			)
		} else {
			instructions = fmt.Sprintf(
				"CHATGPT WORK - DOKONCENI TITULKU\n\n"+
					"1. V ChatGPT Work vyber plugin @VTT CZ Subtitle Workflow.\n"+
					"2. Nahraj tento anglicky VTT soubor:\n%s\n"+
					"3. Zadej: Zpracuj tento soubor a vrat finalni cesky VTT pro vtt-cz.\n"+
					"4. Stahni finalni soubor .cs.vtt a uloz ho presne sem:\n%s\n"+
					"5. Znovu spust: %s\n\n"+
					"Program pak ceske VTT overi a vypali do videa.\n",
				rawEnVTTPath,
				csVTTPath,
				resumeCommand,
			)
		}
		if err := os.WriteFile(instructionsPath, []byte(instructions), 0o644); err != nil {
			return fmt.Errorf("ulozeni navodu pro ChatGPT Work: %w", err)
		}
		if emailMode {
			if err := ensureEmailHandoffFromEnvironment(context.Background(), workDir, rawEnVTTPath, csVTTPath, outputVideoPath); err != nil {
				return fmt.Errorf("odeslani anglickych titulku e-mailem: %w", err)
			}
		}

		fmt.Println()
		fmt.Println("Lokalni prepis je hotovy. Program ted ceka na zpracovani v ChatGPT Work.")
		fmt.Println("Anglicky VTT pro nahrani:", rawEnVTTPath)
		fmt.Println("Cesky VTT uloz presne jako:", csVTTPath)
		fmt.Println("Navod:", instructionsPath)
		if emailMode {
			fmt.Println("Anglicke VTT bylo odeslano na nastavenou e-mailovou adresu.")
			fmt.Println("Finalni .cs.vtt posli jako odpoved na puvodni e-mail.")
			return nil
		}
		fmt.Println("Potom znovu spust:", resumeCommand)
		return nil
	}

	if err := validateVTTFile(csVTTPath); err != nil {
		return fmt.Errorf("vracene ceske VTT neni platne: %w", err)
	}
	if err := runUnlessFileExists(outputVideoPath, "Video s ceskymi titulky uz existuje, preskakuji vypalovani.", func() error {
		return burnSubtitles(videoPath, csVTTPath, outputVideoPath, reelMode)
	}); err != nil {
		return fmt.Errorf("vypalovani titulku: %w", err)
	}

	fmt.Println()
	fmt.Println("Hotovo:")
	fmt.Println("Pracovni slozka:", workDir)
	fmt.Println("Anglicke titulky z lokalniho Whisperu:", rawEnVTTPath)
	fmt.Println("Finalni ceske titulky z ChatGPT Work:", csVTTPath)
	fmt.Println("Video s ceskymi titulky:", outputVideoPath)
	return nil
}

func validateVTTFile(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := strings.TrimPrefix(string(contents), "\ufeff")
	if !strings.HasPrefix(text, "WEBVTT") {
		return fmt.Errorf("soubor nezacina hlavickou WEBVTT")
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	lineNumber := 0
	cueCount := 0
	inCue := false
	cueHasText := false
	var previousEnd time.Duration

	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, "-->") {
			if inCue && line != "" {
				cueHasText = true
			}
			continue
		}

		if inCue && !cueHasText {
			return fmt.Errorf("blok pred radkem %d nema text", lineNumber)
		}
		parts := strings.SplitN(line, "-->", 2)
		start, err := parseVTTTimestamp(strings.TrimSpace(parts[0]))
		if err != nil {
			return fmt.Errorf("radek %d: %w", lineNumber, err)
		}
		endField := strings.Fields(strings.TrimSpace(parts[1]))
		if len(endField) == 0 {
			return fmt.Errorf("radek %d: chybi koncovy cas", lineNumber)
		}
		end, err := parseVTTTimestamp(endField[0])
		if err != nil {
			return fmt.Errorf("radek %d: %w", lineNumber, err)
		}
		if start >= end {
			return fmt.Errorf("radek %d: zacatek titulku neni pred koncem", lineNumber)
		}
		if cueCount > 0 && start < previousEnd {
			return fmt.Errorf("radek %d: titulek se prekryva s predchozim", lineNumber)
		}

		cueCount++
		inCue = true
		cueHasText = false
		previousEnd = end
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if cueCount == 0 {
		return fmt.Errorf("soubor neobsahuje zadny titulkovy blok")
	}
	if !cueHasText {
		return fmt.Errorf("posledni titulkovy blok nema text")
	}
	return nil
}

func parseVTTTimestamp(value string) (time.Duration, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("neplatny cas %q", value)
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("neplatny cas %q", value)
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, fmt.Errorf("neplatny cas %q", value)
	}
	secondParts := strings.Split(parts[2], ".")
	if len(secondParts) != 2 || len(secondParts[1]) != 3 {
		return 0, fmt.Errorf("neplatny cas %q", value)
	}
	seconds, err := strconv.Atoi(secondParts[0])
	if err != nil || seconds < 0 || seconds > 59 {
		return 0, fmt.Errorf("neplatny cas %q", value)
	}
	milliseconds, err := strconv.Atoi(secondParts[1])
	if err != nil {
		return 0, fmt.Errorf("neplatny cas %q", value)
	}

	return time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second +
		time.Duration(milliseconds)*time.Millisecond, nil
}

func createWorkDir() (string, error) {
	workDir := filepath.Join(workRootDir(), time.Now().Format("20060102-150405"))

	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}

	return workDir, nil
}

func workRootDir() string {
	configured := strings.TrimSpace(os.Getenv("VTT_CZ_WORK_ROOT"))
	if configured != "" {
		if absolute, err := filepath.Abs(configured); err == nil {
			return absolute
		}
		return configured
	}
	return filepath.Join(userVideosDir(), "vtt-cz")
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
	ytDLPPath, err := exec.LookPath("yt-dlp")
	if err != nil {
		return "", "", fmt.Errorf("program yt-dlp neni nainstalovany: %w", err)
	}

	if err := updateYTDLP(ytDLPPath); err != nil {
		fmt.Fprintln(os.Stderr, "Varovani: automaticka aktualizace yt-dlp se nepovedla:", err)
		fmt.Fprintln(os.Stderr, "Pokracuji se stavajici verzi yt-dlp.")
	}

	cmd := exec.Command(
		ytDLPPath,
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

func updateYTDLP(ytDLPPath string) error {
	fmt.Println()
	fmt.Println("Kontroluji aktualizaci yt-dlp...")

	cmd := exec.Command(ytDLPPath, "-U")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

type dvdAudioTrack struct {
	Number      int
	Description string
}

type dvdTitle struct {
	Number      int
	Duration    time.Duration
	AudioTracks []dvdAudioTrack
}

func importDVD(reader *bufio.Reader, source string, workDir string) (string, string, error) {
	if _, err := exec.LookPath("HandBrakeCLI"); err != nil {
		return "", "", fmt.Errorf("program HandBrakeCLI neni nainstalovany (na Debianu/Ubuntu nainstaluj balicek handbrake-cli)")
	}

	selectedSource, err := chooseDVDSource(reader, source)
	if err != nil {
		return "", "", err
	}

	fmt.Println()
	fmt.Println("Prohledavam DVD, muze to chvili trvat...")
	titles, scanOutput, err := scanDVDTitles(selectedSource)
	if err != nil {
		return "", "", err
	}
	if len(titles) == 0 {
		return "", "", fmt.Errorf("HandBrake na zdroji %q nenasel zadny DVD titul%s", selectedSource, commandOutputSuffix(scanOutput))
	}

	printDVDTitles(titles)
	selectedTitle, err := chooseDVDTitle(reader, titles)
	if err != nil {
		return "", "", err
	}
	selectedAudio, err := chooseDVDAudioTrack(reader, selectedTitle.AudioTracks)
	if err != nil {
		return "", "", err
	}

	baseName := fmt.Sprintf("dvd-title-%02d", selectedTitle.Number)
	videoPath := filepath.Join(workDir, baseName+".mp4")
	mp3Path := filepath.Join(workDir, baseName+".mp3")

	if err := encodeDVDTitle(selectedSource, selectedTitle.Number, selectedAudio, videoPath); err != nil {
		return "", "", err
	}
	if err := extractMP3(videoPath, mp3Path); err != nil {
		return "", "", err
	}

	return videoPath, mp3Path, nil
}

func chooseDVDSource(reader *bufio.Reader, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return validateDVDSource(strings.TrimSpace(requested))
	}

	candidates := detectDVDDrives()
	if len(candidates) == 1 {
		fmt.Println("Nalezena DVD mechanika:", candidates[0])
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		fmt.Println("Nalezene DVD mechaniky:")
		for i, candidate := range candidates {
			fmt.Printf("  %d) %s\n", i+1, candidate)
		}
		fmt.Print("Vyber mechaniku [1]: ")
		answer, err := readLine(reader)
		if err != nil {
			return "", err
		}
		if answer == "" {
			return candidates[0], nil
		}
		choice, err := strconv.Atoi(answer)
		if err != nil || choice < 1 || choice > len(candidates) {
			return "", fmt.Errorf("neplatna volba mechaniky %q", answer)
		}
		return candidates[choice-1], nil
	}

	fmt.Print("Zadej cestu k DVD mechanice, ISO souboru nebo slozce VIDEO_TS: ")
	answer, err := readLine(reader)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return "", fmt.Errorf("nebyla zadana cesta k DVD")
	}
	return validateDVDSource(answer)
}

func validateDVDSource(source string) (string, error) {
	cleaned := filepath.Clean(source)
	if _, err := os.Stat(cleaned); err != nil {
		return "", fmt.Errorf("DVD zdroj %q nelze otevrit: %w", cleaned, err)
	}
	return cleaned, nil
}

func detectDVDDrives() []string {
	patterns := []string{"/dev/dvd", "/dev/cdrom", "/dev/sr*"}
	seen := make(map[string]bool)
	var candidates []string

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if _, err := os.Stat(match); err != nil {
				continue
			}
			canonical := match
			if resolved, err := filepath.EvalSymlinks(match); err == nil {
				canonical = resolved
			}
			if seen[canonical] {
				continue
			}
			seen[canonical] = true
			candidates = append(candidates, match)
		}
	}

	return candidates
}

func scanDVDTitles(source string) ([]dvdTitle, string, error) {
	cmd := exec.Command("HandBrakeCLI", "--scan", "-t", "0", "-i", source)
	output, err := cmd.CombinedOutput()
	outputText := string(output)
	if err != nil {
		return nil, outputText, fmt.Errorf("sken DVD selhal: %w%s", err, commandOutputSuffix(outputText))
	}

	return parseDVDScan(outputText), outputText, nil
}

func parseDVDScan(output string) []dvdTitle {
	var titles []dvdTitle
	currentTitle := -1
	inAudioTracks := false

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		var number int
		if _, err := fmt.Sscanf(line, "+ title %d:", &number); err == nil {
			titles = append(titles, dvdTitle{Number: number})
			currentTitle = len(titles) - 1
			inAudioTracks = false
			continue
		}
		if currentTitle < 0 {
			continue
		}

		if strings.HasPrefix(line, "+ duration:") {
			durationText := strings.TrimSpace(strings.TrimPrefix(line, "+ duration:"))
			if duration, err := parseClockDuration(durationText); err == nil {
				titles[currentTitle].Duration = duration
			}
			continue
		}
		if line == "+ audio tracks:" {
			inAudioTracks = true
			continue
		}
		if strings.HasPrefix(line, "+ subtitle tracks:") || strings.HasPrefix(line, "+ chapters:") {
			inAudioTracks = false
			continue
		}
		if inAudioTracks {
			track, ok := parseDVDAudioTrack(line)
			if ok {
				titles[currentTitle].AudioTracks = append(titles[currentTitle].AudioTracks, track)
			}
		}
	}

	return titles
}

func parseClockDuration(value string) (time.Duration, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("neplatna delka %q", value)
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	seconds, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, err
	}
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, nil
}

func parseDVDAudioTrack(line string) (dvdAudioTrack, bool) {
	if !strings.HasPrefix(line, "+ ") {
		return dvdAudioTrack{}, false
	}
	line = strings.TrimPrefix(line, "+ ")
	comma := strings.Index(line, ",")
	if comma < 1 {
		return dvdAudioTrack{}, false
	}
	number, err := strconv.Atoi(strings.TrimSpace(line[:comma]))
	if err != nil {
		return dvdAudioTrack{}, false
	}
	return dvdAudioTrack{Number: number, Description: strings.TrimSpace(line[comma+1:])}, true
}

func printDVDTitles(titles []dvdTitle) {
	defaultTitle := longestDVDTitle(titles)
	fmt.Println()
	fmt.Println("Nalezene DVD tituly:")
	for _, title := range titles {
		marker := ""
		if title.Number == defaultTitle.Number {
			marker = "  [pravdepodobne hlavni film]"
		}
		fmt.Printf("  %d) %s%s\n", title.Number, formatDuration(title.Duration), marker)
	}
}

func chooseDVDTitle(reader *bufio.Reader, titles []dvdTitle) (dvdTitle, error) {
	defaultTitle := longestDVDTitle(titles)
	fmt.Printf("Vyber cislo DVD titulu [%d]: ", defaultTitle.Number)
	answer, err := readLine(reader)
	if err != nil {
		return dvdTitle{}, err
	}
	if answer == "" {
		return defaultTitle, nil
	}

	selectedNumber, err := strconv.Atoi(answer)
	if err != nil {
		return dvdTitle{}, fmt.Errorf("neplatne cislo DVD titulu %q", answer)
	}
	for _, title := range titles {
		if title.Number == selectedNumber {
			return title, nil
		}
	}
	return dvdTitle{}, fmt.Errorf("DVD titul %d nebyl pri skenu nalezen", selectedNumber)
}

func chooseDVDAudioTrack(reader *bufio.Reader, tracks []dvdAudioTrack) (int, error) {
	if len(tracks) == 0 {
		fmt.Println("Sken nevypsal zvukove stopy, HandBrake pouzije prvni dostupnou stopu.")
		return 0, nil
	}

	fmt.Println("Zvukove stopy vybraneho titulu:")
	for _, track := range tracks {
		fmt.Printf("  %d) %s\n", track.Number, track.Description)
	}
	fmt.Printf("Vyber zvukovou stopu [%d]: ", tracks[0].Number)
	answer, err := readLine(reader)
	if err != nil {
		return 0, err
	}
	if answer == "" {
		return tracks[0].Number, nil
	}

	selectedNumber, err := strconv.Atoi(answer)
	if err != nil {
		return 0, fmt.Errorf("neplatne cislo zvukove stopy %q", answer)
	}
	for _, track := range tracks {
		if track.Number == selectedNumber {
			return selectedNumber, nil
		}
	}
	return 0, fmt.Errorf("zvukova stopa %d nebyla u vybraneho titulu nalezena", selectedNumber)
}

func longestDVDTitle(titles []dvdTitle) dvdTitle {
	longest := titles[0]
	for _, title := range titles[1:] {
		if title.Duration > longest.Duration {
			longest = title
		}
	}
	return longest
}

func formatDuration(duration time.Duration) string {
	seconds := int64(duration / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, (seconds/60)%60, seconds%60)
}

func encodeDVDTitle(source string, titleNumber int, audioTrack int, outputPath string) error {
	fmt.Println()
	fmt.Printf("Prevadim DVD titul %d do MP4...\n", titleNumber)

	args := []string{
		"-i", source,
		"-t", strconv.Itoa(titleNumber),
		"-o", outputPath,
		"-f", "av_mp4",
		"-e", "x264",
		"-q", "20",
		"--encoder-preset", "medium",
		"-E", "av_aac",
		"-B", "160",
		"--mixdown", "stereo",
		"--markers",
		"--optimize",
	}
	if audioTrack > 0 {
		args = append(args, "-a", strconv.Itoa(audioTrack))
	}

	cmd := exec.Command("HandBrakeCLI", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("prevod DVD do MP4 selhal: %w", err)
	}
	if !fileExists(outputPath) {
		return fmt.Errorf("HandBrake skoncil bez chyby, ale nevytvoril %q", outputPath)
	}
	return nil
}

func extractMP3(videoPath string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vytvarim MP3 pro prepis...")

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", videoPath,
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-b:a", "64k",
		outputPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vytvoreni MP3 selhalo: %w", err)
	}
	return nil
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func commandOutputSuffix(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ""
	}
	const maxLength = 4000
	if len(trimmed) > maxLength {
		trimmed = trimmed[len(trimmed)-maxLength:]
	}
	return "\nPosledni vystup HandBrake:\n" + trimmed
}

func createEnglishVTT(apiKey string, mp3Path string, outputPath string, localWhisper bool) error {
	if localWhisper {
		return createEnglishVTTLocal(mp3Path, outputPath)
	}
	return createEnglishVTTCloud(apiKey, mp3Path, outputPath)
}

func createEnglishVTTLocal(mp3Path string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vytvarim anglicke VTT titulky lokalne pres whisper.cpp, Silero VAD a DTW...")

	config := localwhisper.DefaultConfig(mp3Path, outputPath)
	config.Language = "en"
	config.Threads = 1
	if err := localwhisper.Transcribe(context.Background(), config); err != nil {
		return fmt.Errorf("lokalni prepis pres Whisper selhal: %w", err)
	}
	if !fileExists(outputPath) {
		return fmt.Errorf("lokalni Whisper skoncil bez chyby, ale nevytvoril %q", outputPath)
	}
	return nil
}

func createEnglishVTTCloud(apiKey string, mp3Path string, outputPath string) error {
	fmt.Println()
	fmt.Println("Vytvarim anglicke VTT titulky pres OpenAI Whisper...")

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
	if outputInfo, err := os.Stat(outputPath); err == nil && outputInfo.Size() > 0 && outputInfo.Size() <= maxTranscriptionBytes {
		fmt.Println("Audio pro Whisper uz existuje, pouzivam:", outputPath)
		return outputPath, nil
	}

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

func burnSubtitles(videoPath string, subtitlesPath string, outputPath string, reelMode bool) error {
	fmt.Println()
	fmt.Println("Vypaluji ceske titulky do videa...")

	workDir := filepath.Dir(subtitlesPath)
	temporarySubtitles, err := os.CreateTemp(workDir, ".vtt-cz-subtitles-*.vtt")
	if err != nil {
		return err
	}
	temporarySubtitlesPath := temporarySubtitles.Name()
	defer os.Remove(temporarySubtitlesPath)

	subtitles, err := os.Open(subtitlesPath)
	if err != nil {
		temporarySubtitles.Close()
		return err
	}
	if _, err := io.Copy(temporarySubtitles, subtitles); err != nil {
		subtitles.Close()
		temporarySubtitles.Close()
		return err
	}
	if err := subtitles.Close(); err != nil {
		temporarySubtitles.Close()
		return err
	}
	if err := temporarySubtitles.Close(); err != nil {
		return err
	}

	temporaryOutput, err := os.CreateTemp(filepath.Dir(outputPath), ".vtt-cz-output-*.mp4")
	if err != nil {
		return err
	}
	temporaryOutputPath := temporaryOutput.Name()
	if err := temporaryOutput.Close(); err != nil {
		return err
	}
	if err := os.Remove(temporaryOutputPath); err != nil {
		return err
	}
	defer os.Remove(temporaryOutputPath)

	style := subtitleBurnStyle(reelMode)
	filter := fmt.Sprintf(
		"subtitles=filename=%s:force_style=%s",
		filepath.Base(temporarySubtitlesPath),
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
		temporaryOutputPath,
	)

	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return err
	}

	return os.Rename(temporaryOutputPath, outputPath)
}

func subtitleBurnStyle(reelMode bool) string {
	if reelMode {
		return reelSubtitleStyle
	}
	return standardSubtitleStyle
}

func printUsage() {
	fmt.Println("Pouziti:")
	fmt.Println("  vtt-cz")
	fmt.Println("  vtt-cz \"URL_VIDEA\"")
	fmt.Println("  vtt-cz --local-whisper [\"URL_VIDEA\"]")
	fmt.Println("  vtt-cz --reel [\"URL_VIDEA\"]")
	fmt.Println("  vtt-cz --work [\"URL_VIDEA\"]")
	fmt.Println("  vtt-cz --work --reel [\"URL_VIDEA\"]")
	fmt.Println("  vtt-cz --work --email [\"URL_VIDEA\"]")
	fmt.Println("  vtt-cz --email-listen")
	fmt.Println("  vtt-cz --dvd")
	fmt.Println("  vtt-cz --dvd \"/cesta/k/DVD_nebo_ISO\"")
	fmt.Println("  vtt-cz --local-whisper --dvd [\"/cesta/k/DVD_nebo_ISO\"]")
	fmt.Println("  vtt-cz --work --dvd [\"/cesta/k/DVD_nebo_ISO\"]")
}

func executableCommand(arguments ...string) string {
	executable := "vtt-cz"
	if path, err := os.Executable(); err == nil && !strings.Contains(path, string(filepath.Separator)+"go-build") {
		executable = strconv.Quote(path)
	}
	if len(arguments) == 0 {
		return executable
	}
	return executable + " " + strings.Join(arguments, " ")
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

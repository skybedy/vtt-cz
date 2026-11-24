package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseArguments(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantMode     string
		wantInput    string
		wantLocal    bool
		wantWork     bool
		wantReel     bool
		wantEmail    bool
		wantListener bool
		wantDoctor   bool
		wantError    bool
	}{
		{name: "interactive URL", wantMode: "url"},
		{name: "URL", args: []string{"https://example.com/video"}, wantMode: "url", wantInput: "https://example.com/video"},
		{name: "local Whisper interactive URL", args: []string{"--local-whisper"}, wantMode: "url", wantLocal: true},
		{name: "local Whisper URL", args: []string{"--local-whisper", "https://example.com/video"}, wantMode: "url", wantInput: "https://example.com/video", wantLocal: true},
		{name: "Work interactive URL", args: []string{"--work"}, wantMode: "url", wantWork: true},
		{name: "Work URL", args: []string{"--work", "https://example.com/video"}, wantMode: "url", wantInput: "https://example.com/video", wantWork: true},
		{name: "Reel URL", args: []string{"--reel", "https://example.com/video"}, wantMode: "url", wantInput: "https://example.com/video", wantReel: true},
		{name: "Work Reel", args: []string{"--work", "--reel"}, wantMode: "url", wantWork: true, wantReel: true},
		{name: "Work email", args: []string{"--work", "--email", "https://example.com/video"}, wantMode: "url", wantInput: "https://example.com/video", wantWork: true, wantEmail: true},
		{name: "email listener", args: []string{"--email-listen"}, wantMode: "url", wantListener: true},
		{name: "doctor Work email", args: []string{"--doctor", "--work", "--email"}, wantMode: "url", wantWork: true, wantEmail: true, wantDoctor: true},
		{name: "doctor listener", args: []string{"--doctor", "--email-listen"}, wantMode: "url", wantListener: true, wantDoctor: true},
		{name: "detected DVD", args: []string{"--dvd"}, wantMode: "dvd"},
		{name: "explicit DVD", args: []string{"--dvd", "/dev/sr0"}, wantMode: "dvd", wantInput: "/dev/sr0"},
		{name: "local Whisper DVD", args: []string{"--local-whisper", "--dvd", "/dev/sr0"}, wantMode: "dvd", wantInput: "/dev/sr0", wantLocal: true},
		{name: "local Whisper after DVD", args: []string{"--dvd", "--local-whisper", "/dev/sr0"}, wantMode: "dvd", wantInput: "/dev/sr0", wantLocal: true},
		{name: "Work DVD", args: []string{"--work", "--dvd", "/dev/sr0"}, wantMode: "dvd", wantInput: "/dev/sr0", wantWork: true},
		{name: "unknown flag", args: []string{"--unknown"}, wantError: true},
		{name: "too many arguments", args: []string{"one", "two"}, wantError: true},
		{name: "duplicate local Whisper", args: []string{"--local-whisper", "--local-whisper"}, wantError: true},
		{name: "duplicate Work", args: []string{"--work", "--work"}, wantError: true},
		{name: "duplicate Reel", args: []string{"--reel", "--reel"}, wantError: true},
		{name: "duplicate email", args: []string{"--email", "--email"}, wantError: true},
		{name: "duplicate doctor", args: []string{"--doctor", "--doctor"}, wantError: true},
		{name: "doctor with input", args: []string{"--doctor", "https://example.com/video"}, wantError: true},
		{name: "email without Work", args: []string{"--email"}, wantError: true},
		{name: "email listener with Work", args: []string{"--email-listen", "--work"}, wantError: true},
		{name: "email listener with input", args: []string{"--email-listen", "https://example.com/video"}, wantError: true},
		{name: "Work and local Whisper", args: []string{"--work", "--local-whisper"}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseArguments(test.args)
			if (err != nil) != test.wantError {
				t.Fatalf("parseArguments() error = %v, wantError %v", err, test.wantError)
			}
			if options.mode != test.wantMode || options.input != test.wantInput || options.localWhisper != test.wantLocal || options.workMode != test.wantWork || options.reelMode != test.wantReel || options.emailMode != test.wantEmail || options.emailListener != test.wantListener || options.doctor != test.wantDoctor {
				t.Fatalf(
					"parseArguments() = %#v, want mode=%q input=%q local=%v work=%v reel=%v email=%v listener=%v doctor=%v",
					options,
					test.wantMode,
					test.wantInput,
					test.wantLocal,
					test.wantWork,
					test.wantReel,
					test.wantEmail,
					test.wantListener,
					test.wantDoctor,
				)
			}
		})
	}
}

func TestDoctorWorkEmailEnvironment(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe", "nice", "ionice", "yt-dlp", "node", "whisper-cli", "vad-speech-segments", "sendmail"} {
		writeTestExecutable(t, filepath.Join(binDir, name), "#!/bin/sh\nexit 0\n")
	}
	modelDir := t.TempDir()
	whisperModel := filepath.Join(modelDir, "whisper.bin")
	vadModel := filepath.Join(modelDir, "vad.bin")
	for _, path := range []string{whisperModel, vadModel} {
		if err := os.WriteFile(path, []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	maildir := filepath.Join(t.TempDir(), "Maildir")
	for _, directory := range []string{"new", "cur"} {
		if err := os.MkdirAll(filepath.Join(maildir, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("PATH", binDir)
	t.Setenv("VTT_CZ_WHISPER_BIN", filepath.Join(binDir, "whisper-cli"))
	t.Setenv("VTT_CZ_VAD_BIN", filepath.Join(binDir, "vad-speech-segments"))
	t.Setenv("VTT_CZ_WHISPER_MODEL", whisperModel)
	t.Setenv("VTT_CZ_VAD_MODEL", vadModel)
	t.Setenv("VTT_CZ_YTDLP_COOKIES_FROM_BROWSER", "none")
	t.Setenv("VTT_CZ_YTDLP_JS_RUNTIMES", "node:"+filepath.Join(binDir, "node"))
	t.Setenv("VTT_CZ_EMAIL_TRANSPORT", "local")
	t.Setenv("VTT_CZ_EMAIL_FROM", "bot@example.com")
	t.Setenv("VTT_CZ_EMAIL_TO", "user@example.net")
	t.Setenv("VTT_CZ_EMAIL_ALLOWED_FROM", "user@example.net")
	t.Setenv("VTT_CZ_EMAIL_SENDMAIL", filepath.Join(binDir, "sendmail"))
	t.Setenv("VTT_CZ_EMAIL_MAILDIR", maildir)

	var output bytes.Buffer
	if err := runDoctor(cliOptions{mode: "url", workMode: true, emailMode: true}, &output); err != nil {
		t.Fatalf("runDoctor() error = %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "Prostredi je pro zvoleny rezim pripravene") || !strings.Contains(output.String(), "yt-dlp cookies: vypnute") {
		t.Fatalf("doctor output = %q", output.String())
	}
}

func TestYTDLPEnvironmentOptions(t *testing.T) {
	t.Setenv("VTT_CZ_YTDLP_COOKIES_FROM_BROWSER", "none")
	t.Setenv("VTT_CZ_YTDLP_AUTO_UPDATE", "false")
	t.Setenv("VTT_CZ_YTDLP_JS_RUNTIMES", "node:/opt/node")
	if arguments := ytDLPCookieArguments(); len(arguments) != 0 {
		t.Fatalf("ytDLPCookieArguments() = %#v", arguments)
	}
	if ytDLPAutoUpdateEnabled() {
		t.Fatal("ytDLPAutoUpdateEnabled() = true")
	}
	if arguments := ytDLPJSRuntimeArguments(); len(arguments) != 2 || arguments[0] != "--js-runtimes" || arguments[1] != "node:/opt/node" {
		t.Fatalf("ytDLPJSRuntimeArguments() = %#v", arguments)
	}
	downloadArguments := ytDLPDownloadArguments("https://example.com/video")
	joined := strings.Join(downloadArguments, " ")
	if !strings.Contains(joined, "--format "+ytDLPVideoFormat) || !strings.Contains(joined, "--js-runtimes node:/opt/node") {
		t.Fatalf("ytDLPDownloadArguments() = %#v", downloadArguments)
	}

	t.Setenv("VTT_CZ_YTDLP_COOKIES_FROM_BROWSER", "firefox:default-release")
	t.Setenv("VTT_CZ_YTDLP_AUTO_UPDATE", "true")
	arguments := ytDLPCookieArguments()
	if len(arguments) != 2 || arguments[0] != "--cookies-from-browser" || arguments[1] != "firefox:default-release" {
		t.Fatalf("ytDLPCookieArguments() = %#v", arguments)
	}
	if !ytDLPAutoUpdateEnabled() {
		t.Fatal("ytDLPAutoUpdateEnabled() = false")
	}
}

func TestLoadEnvironmentFilePreservesExistingEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "email.env")
	if err := os.WriteFile(path, []byte("# test\nVTT_CZ_TEST_FROM_FILE='loaded value'\nVTT_CZ_TEST_OVERRIDE=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"VTT_CZ_TEST_FROM_FILE", "VTT_CZ_TEST_OVERRIDE"} {
		oldValue, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, oldValue)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	if err := os.Setenv("VTT_CZ_TEST_OVERRIDE", "from-shell"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VTT_CZ_ENV_FILE", path)
	if err := loadRuntimeEnvironment(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("VTT_CZ_TEST_FROM_FILE"); got != "loaded value" {
		t.Fatalf("VTT_CZ_TEST_FROM_FILE = %q", got)
	}
	if got := os.Getenv("VTT_CZ_TEST_OVERRIDE"); got != "from-shell" {
		t.Fatalf("VTT_CZ_TEST_OVERRIDE = %q", got)
	}
}

func TestLoadEnvironmentFileRejectsWritableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "email.env")
	if err := os.WriteFile(path, []byte("VTT_CZ_TEST=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvironmentFile(path); err == nil {
		t.Fatal("loadEnvironmentFile() accepted a group/world-writable file")
	}
}

func TestValidateVTTFile(t *testing.T) {
	tests := []struct {
		name      string
		contents  string
		wantError bool
	}{
		{
			name: "valid",
			contents: "WEBVTT\n\n" +
				"00:00:00.000 --> 00:00:01.500\nAhoj.\n\n" +
				"00:00:01.500 --> 00:00:03.000\nJak se mas?\n",
		},
		{name: "missing header", contents: "00:00:00.000 --> 00:00:01.000\nAhoj.\n", wantError: true},
		{name: "overlap", contents: "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nAhoj.\n\n00:00:01.500 --> 00:00:03.000\nDalsi.\n", wantError: true},
		{name: "empty cue", contents: "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\n", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "subtitles.vtt")
			if err := os.WriteFile(path, []byte(test.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			err := validateVTTFile(path)
			if (err != nil) != test.wantError {
				t.Fatalf("validateVTTFile() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestUpdateYTDLP(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "yt-dlp.log")
	ytDLPPath := filepath.Join(binDir, "yt-dlp")

	writeTestExecutable(t, ytDLPPath, `#!/bin/sh
printf '%s\n' "$*" > "$VTT_CZ_TEST_LOG"
`)
	t.Setenv("VTT_CZ_TEST_LOG", logPath)

	if err := updateYTDLP(ytDLPPath); err != nil {
		t.Fatalf("updateYTDLP() error = %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(logBytes)) != "-U" {
		t.Fatalf("updateYTDLP() invoked yt-dlp with %q, want %q", strings.TrimSpace(string(logBytes)), "-U")
	}
}

func TestUpdateYTDLPReturnsError(t *testing.T) {
	ytDLPPath := filepath.Join(t.TempDir(), "yt-dlp")
	writeTestExecutable(t, ytDLPPath, `#!/bin/sh
exit 1
`)

	if err := updateYTDLP(ytDLPPath); err == nil {
		t.Fatal("updateYTDLP() error = nil, want an error")
	}
}

func TestRunWorkModeWaitsForCzechVTT(t *testing.T) {
	workDir := t.TempDir()
	videoPath := filepath.Join(workDir, "video.mp4")
	mp3Path := filepath.Join(workDir, "video.mp3")
	rawEnVTTPath := filepath.Join(workDir, "video.en.raw.vtt")
	csVTTPath := filepath.Join(workDir, "video.cs.vtt")
	outputVideoPath := filepath.Join(workDir, "video.cs.mp4")

	for path, contents := range map[string]string{
		videoPath:    "fake video",
		mp3Path:      "fake audio",
		rawEnVTTPath: "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := runWorkMode(workDir, videoPath, mp3Path, rawEnVTTPath, csVTTPath, outputVideoPath, true, false); err != nil {
		t.Fatalf("runWorkMode() error = %v", err)
	}
	instructionsPath := filepath.Join(workDir, "CHATGPT-WORK.txt")
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(instructions), rawEnVTTPath) || !strings.Contains(string(instructions), csVTTPath) || !strings.Contains(string(instructions), "--work --reel") {
		t.Fatalf("Work instructions do not contain handoff paths:\n%s", instructions)
	}
}

func TestRunWorkModeAcceptsCompletedCzechVTT(t *testing.T) {
	workDir := t.TempDir()
	videoPath := filepath.Join(workDir, "video.mp4")
	mp3Path := filepath.Join(workDir, "video.mp3")
	rawEnVTTPath := filepath.Join(workDir, "video.en.raw.vtt")
	csVTTPath := filepath.Join(workDir, "video.cs.vtt")
	outputVideoPath := filepath.Join(workDir, "video.cs.mp4")

	for path, contents := range map[string]string{
		videoPath:       "fake video",
		mp3Path:         "fake audio",
		rawEnVTTPath:    "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nHello.\n",
		csVTTPath:       "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nAhoj.\n",
		outputVideoPath: "fake completed video",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := runWorkMode(workDir, videoPath, mp3Path, rawEnVTTPath, csVTTPath, outputVideoPath, false, false); err != nil {
		t.Fatalf("runWorkMode() error = %v", err)
	}
}

func TestSubtitleBurnStyle(t *testing.T) {
	if style := subtitleBurnStyle(false); !strings.Contains(style, "FontSize=16") || !strings.Contains(style, "Outline=4") {
		t.Fatalf("standard subtitle style = %q", style)
	}
	if style := subtitleBurnStyle(true); !strings.Contains(style, "FontSize=9") || !strings.Contains(style, "Outline=2") || !strings.Contains(style, "MarginV=28") {
		t.Fatalf("Reel subtitle style = %q", style)
	}
}

func TestCreateEnglishVTTLocal(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	inputPath := filepath.Join(workDir, "audio.mp3")
	outputPath := filepath.Join(workDir, "audio.en.raw.vtt")
	logPath := filepath.Join(workDir, "local-whisper.log")
	whisperPath := filepath.Join(binDir, "whisper-cli")
	vadPath := filepath.Join(binDir, "vad-speech-segments")
	modelPath := filepath.Join(workDir, "ggml-large-v3.bin")
	vadModelPath := filepath.Join(workDir, "silero.bin")

	if err := os.WriteFile(inputPath, []byte("fake MP3"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{modelPath, vadModelPath} {
		if err := os.WriteFile(path, []byte("fake model"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeTestExecutable(t, filepath.Join(binDir, "ffprobe"), `#!/bin/sh
printf 'ffprobe %s\n' "$*" >> "$VTT_CZ_TEST_LOG"
printf '10.000\n'
`)
	writeTestExecutable(t, filepath.Join(binDir, "ffmpeg"), `#!/bin/sh
printf 'ffmpeg %s\n' "$*" >> "$VTT_CZ_TEST_LOG"
for output do :; done
printf 'fake wav\n' > "$output"
`)
	writeTestExecutable(t, vadPath, `#!/bin/sh
printf 'vad %s\n' "$*" >> "$VTT_CZ_TEST_LOG"
printf 'start = 100.00, end = 300.00\n'
`)
	writeTestExecutable(t, whisperPath, `#!/bin/sh
printf 'whisper %s\n' "$*" >> "$VTT_CZ_TEST_LOG"
for argument do
  case "$argument" in
    *.wav)
      printf 'WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello.\n' > "$argument.vtt"
      printf '{"transcription":[{"tokens":[{"text":" Hello","t_dtw":100},{"text":".","t_dtw":150}]}]}\n' > "$argument.json"
      ;;
  esac
done
`)
	t.Setenv("PATH", binDir)
	t.Setenv("VTT_CZ_TEST_LOG", logPath)
	t.Setenv("VTT_CZ_WHISPER_BIN", whisperPath)
	t.Setenv("VTT_CZ_VAD_BIN", vadPath)
	t.Setenv("VTT_CZ_WHISPER_MODEL", modelPath)
	t.Setenv("VTT_CZ_VAD_MODEL", vadModelPath)

	if err := createEnglishVTTLocal(inputPath, outputPath); err != nil {
		t.Fatalf("createEnglishVTTLocal() error = %v", err)
	}
	if !fileExists(outputPath) {
		t.Fatalf("local Whisper output %q was not created", outputPath)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	for _, expected := range []string{"ffprobe", "ffmpeg", "vad", "whisper", "-l en", "-t 1", "-dtw large.v3"} {
		if !strings.Contains(logText, expected) {
			t.Fatalf("local Whisper command log %q does not contain %q", logText, expected)
		}
	}
	if strings.Contains(strings.ToLower(logText), "python") {
		t.Fatalf("local Whisper unexpectedly invoked Python:\n%s", logText)
	}
}

func TestParseDVDScan(t *testing.T) {
	output := `
    + title 1:
      + duration: 00:04:20
      + chapters:
        + 1: cells 0->0, 100 blocks, duration 00:04:20
      + audio tracks:
        + 1, English (AC3) (2.0 ch) (iso639-2: eng)
        + 2, Czech (AC3) (2.0 ch) (iso639-2: ces)
      + subtitle tracks:
        + 1, English [VOBSUB]
    + title 3:
      + duration: 01:42:09
      + audio tracks:
        + 1, English (AC3) (5.1 ch) (iso639-2: eng)
`

	titles := parseDVDScan(output)
	if len(titles) != 2 {
		t.Fatalf("parseDVDScan() returned %d titles, want 2", len(titles))
	}
	if titles[0].Number != 1 || titles[0].Duration != 4*time.Minute+20*time.Second {
		t.Fatalf("first title = %#v", titles[0])
	}
	if len(titles[0].AudioTracks) != 2 || titles[0].AudioTracks[1].Number != 2 {
		t.Fatalf("first title audio tracks = %#v", titles[0].AudioTracks)
	}
	if titles[1].Number != 3 || titles[1].Duration != time.Hour+42*time.Minute+9*time.Second {
		t.Fatalf("second title = %#v", titles[1])
	}
	if longestDVDTitle(titles).Number != 3 {
		t.Fatalf("longestDVDTitle() = %d, want 3", longestDVDTitle(titles).Number)
	}
}

func TestImportDVD(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "movie.iso")
	logPath := filepath.Join(t.TempDir(), "commands.log")
	if err := os.WriteFile(sourcePath, []byte("fake DVD"), 0o644); err != nil {
		t.Fatal(err)
	}

	handBrakeScript := `#!/bin/sh
printf 'HandBrakeCLI %s\n' "$*" >> "$VTT_CZ_TEST_LOG"
case " $* " in
  *" --scan "*)
    printf '%s\n' \
      '    + title 1:' \
      '      + duration: 00:05:00' \
      '      + audio tracks:' \
      '        + 1, English (AC3) (2.0 ch)' \
      '    + title 2:' \
      '      + duration: 01:30:00' \
      '      + audio tracks:' \
      '        + 1, English (AC3) (5.1 ch)'
    exit 0
    ;;
esac
output=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then
    shift
    output="$1"
    break
  fi
  shift
done
printf 'fake MP4' > "$output"
`
	ffmpegScript := `#!/bin/sh
printf 'ffmpeg %s\n' "$*" >> "$VTT_CZ_TEST_LOG"
for output do :; done
printf 'fake MP3' > "$output"
`
	writeTestExecutable(t, filepath.Join(binDir, "HandBrakeCLI"), handBrakeScript)
	writeTestExecutable(t, filepath.Join(binDir, "ffmpeg"), ffmpegScript)
	t.Setenv("PATH", binDir)
	t.Setenv("VTT_CZ_TEST_LOG", logPath)

	reader := bufio.NewReader(strings.NewReader("\n\n"))
	videoPath, mp3Path, err := importDVD(reader, sourcePath, workDir)
	if err != nil {
		t.Fatalf("importDVD() error = %v", err)
	}
	if filepath.Base(videoPath) != "dvd-title-02.mp4" || !fileExists(videoPath) {
		t.Fatalf("videoPath = %q", videoPath)
	}
	if filepath.Base(mp3Path) != "dvd-title-02.mp3" || !fileExists(mp3Path) {
		t.Fatalf("mp3Path = %q", mp3Path)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logBytes)
	if !strings.Contains(logText, "-t 2") || !strings.Contains(logText, "-a 1") {
		t.Fatalf("HandBrake command did not contain selected title and audio:\n%s", logText)
	}
}

func writeTestExecutable(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

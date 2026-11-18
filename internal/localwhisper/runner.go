package localwhisper

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type transcriptionOutput struct {
	vttPath  string
	jsonPath string
	hasJSON  bool
}

// Transcribe runs the local VAD-windowed whisper.cpp workflow entirely from
// Go. External media/ML tools are still executed as child processes, but no
// Python interpreter or Python source file is used at runtime.
func Transcribe(ctx context.Context, config Config) error {
	config.InputPath = expandHome(config.InputPath)
	config.OutputPath = expandHome(config.OutputPath)
	config.WhisperBin = expandHome(config.WhisperBin)
	config.VADBin = expandHome(config.VADBin)
	config.ModelPath = expandHome(config.ModelPath)
	config.VADModel = expandHome(config.VADModel)
	if config.VADLogPath != "" {
		config.VADLogPath = expandHome(config.VADLogPath)
	}

	if err := validateConfig(config); err != nil {
		return err
	}
	for _, command := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(command); err != nil {
			return fmt.Errorf("missing required command on PATH: %s", command)
		}
	}
	for path, label := range map[string]string{
		config.InputPath:  "input media",
		config.WhisperBin: "whisper.cpp whisper-cli binary",
		config.VADBin:     "whisper.cpp vad-speech-segments binary",
		config.ModelPath:  "Whisper model",
		config.VADModel:   "Silero VAD model",
	} {
		if err := requireRegularFile(path, label); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(config.OutputPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	durationMS, err := probeDurationMS(ctx, config, config.InputPath)
	if err != nil {
		return err
	}
	logf("Source duration: %s", msToTimestamp(durationMS))

	workDir, err := os.MkdirTemp("", "local-whisper-vad-")
	if err != nil {
		return fmt.Errorf("create local Whisper temp directory: %w", err)
	}
	transcriptionStarted := false
	success := false
	defer func() {
		if config.KeepTemp || transcriptionStarted && !success {
			reason := "requested"
			if !config.KeepTemp {
				reason = "post-transcription failure"
			}
			logf("Keeping temp directory after %s: %s", reason, workDir)
			return
		}
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			logf("Warning: could not remove temp directory %s: %v", workDir, removeErr)
		}
	}()

	sourceWAV := filepath.Join(workDir, "source.wav")
	if err := makeWAV(ctx, config, config.InputPath, sourceWAV); err != nil {
		return err
	}
	vadOutput, err := runVAD(ctx, config, sourceWAV, workDir)
	if err != nil {
		return err
	}
	if config.VADLogPath != "" {
		if err := os.MkdirAll(filepath.Dir(config.VADLogPath), 0o755); err != nil {
			return fmt.Errorf("create VAD log directory: %w", err)
		}
		if err := os.WriteFile(config.VADLogPath, []byte(vadOutput), 0o644); err != nil {
			return fmt.Errorf("write VAD log: %w", err)
		}
	}

	segments := convertVADUnits(parseVADCandidates(vadOutput), durationMS)
	logf("VAD speech segments: %d", len(segments))

	var windows []window
	if config.WindowMode == "fixed" {
		windows, err = buildFixedWindows(durationMS, config.FixedWindowMS, maxInt(0, config.FixedOverlapMS))
		if err != nil {
			return err
		}
	} else {
		windows = mergeSegments(
			segments,
			durationMS,
			maxInt(0, config.PrePadMS),
			maxInt(0, config.PostPadMS),
			maxInt(0, config.LongGapPreRollMS),
			maxInt(0, config.LongGapThresholdMS),
			maxInt(0, config.MergeGapMS),
			config.MaxWindowMS,
		)
	}
	logf("Transcription windows: %d", len(windows))
	if config.MaxWindows > 0 && len(windows) > config.MaxWindows {
		logf("Limiting transcription windows to first %d for this run.", config.MaxWindows)
		windows = windows[:config.MaxWindows]
	}
	if len(windows) == 0 {
		return fmt.Errorf("VAD detected no speech windows")
	}
	for index, item := range windows {
		fmt.Fprintf(os.Stderr, "%04d\t%s\t%s\n", index+1, msToTimestamp(item.startMS), msToTimestamp(item.endMS))
	}
	if config.DryRunVAD {
		logf("Dry run requested; no transcription or VTT output was created.")
		success = true
		return nil
	}

	wavPaths := make([]string, 0, len(windows))
	for index, item := range windows {
		wavPath := filepath.Join(workDir, fmt.Sprintf("window_%04d.wav", index+1))
		if err := cutWindow(ctx, config, sourceWAV, wavPath, item); err != nil {
			return err
		}
		wavPaths = append(wavPaths, wavPath)
	}

	transcriptionStarted = true
	outputs, err := transcribeWindows(ctx, config, wavPaths, workDir)
	if err != nil {
		return err
	}

	var allCues []cue
	for index, output := range outputs {
		cues, err := parseVTT(output.vttPath)
		if err != nil {
			return fmt.Errorf("parse window VTT %s: %w", output.vttPath, err)
		}
		var bounds []dtwBound
		hasBounds := false
		if output.hasJSON {
			bounds, hasBounds = parseDTWBounds(output.jsonPath, len(cues))
		}
		windowCues, err := shiftAndGuardCues(cues, windows[index], config, bounds, hasBounds)
		if err != nil {
			return err
		}
		if !config.KeepMusicCues {
			kept := windowCues[:0]
			for _, item := range windowCues {
				if !isNonSpeechCue(item) {
					kept = append(kept, item)
				}
			}
			if removed := len(windowCues) - len(kept); removed > 0 {
				logf("Filtered %d standalone music cue(s) from this window.", removed)
			}
			windowCues = kept
		}
		allCues = append(allCues, windowCues...)
	}

	sortCues(allCues)
	if !config.NoDeduplicateOverlaps {
		var removed int
		allCues, removed = deduplicateOverlappingCues(allCues)
		if removed > 0 {
			logf("Removed repeated text from %d overlapping cue(s).", removed)
		}
		allCues, removed = dropAdjacentRepeatedFragments(allCues)
		if removed > 0 {
			logf("Dropped %d adjacent repeated fragment cue(s).", removed)
		}
	}
	var droppedNested, shortenedPrevious int
	allCues, droppedNested, shortenedPrevious, err = resolveOverlappingCues(allCues, 750)
	if err != nil {
		return err
	}
	if droppedNested > 0 {
		logf("Dropped %d duplicate or very short nested cue(s).", droppedNested)
	}
	if shortenedPrevious > 0 {
		logf("Shortened %d cue(s) to remove remaining time overlaps.", shortenedPrevious)
	}
	if err := validateCues(allCues, durationMS); err != nil {
		return err
	}
	if err := writeVTT(config.OutputPath, allCues); err != nil {
		return err
	}
	logf("Wrote %d subtitle cues to %s", len(allCues), config.OutputPath)
	logCue("First cue", allCues[0])
	logCue("Last cue", allCues[len(allCues)-1])
	success = true
	return nil
}

func validateConfig(config Config) error {
	if config.InputPath == "" || config.OutputPath == "" {
		return fmt.Errorf("local Whisper input and output paths are required")
	}
	if config.WhisperBin == "" || config.VADBin == "" || config.ModelPath == "" || config.VADModel == "" {
		return fmt.Errorf("local Whisper binary and model paths are required")
	}
	if config.Language == "" {
		return fmt.Errorf("local Whisper language is required")
	}
	if config.Threads <= 0 || config.VADMinSpeechMS <= 0 || config.VADMinSilenceMS <= 0 || config.MaxWindowMS <= 0 || config.FixedWindowMS <= 0 || config.WhisperBeamSize <= 0 || config.WhisperBestOf <= 0 {
		return fmt.Errorf("local Whisper positive numeric settings must be greater than zero")
	}
	if config.WindowMode != "vad" && config.WindowMode != "fixed" {
		return fmt.Errorf("unknown local Whisper window mode %q", config.WindowMode)
	}
	if config.MaxWindows < 0 {
		return fmt.Errorf("local Whisper max windows cannot be negative")
	}
	return nil
}

func requireRegularFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("missing %s: %s: %w", label, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a file: %s", label, path)
	}
	return nil
}

func probeDurationMS(ctx context.Context, config Config, mediaPath string) (int, error) {
	stdout, stderr, err := runCaptured(ctx, config, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		mediaPath,
	)
	if err != nil {
		return 0, commandError("ffprobe", err, stdout, stderr)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(stdout), 64)
	if err != nil {
		return 0, fmt.Errorf("could not parse ffprobe duration for %s: %q", mediaPath, strings.TrimSpace(stdout))
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid media duration from ffprobe: %v", duration)
	}
	return int(math.RoundToEven(duration * 1000)), nil
}

func makeWAV(ctx context.Context, config Config, inputPath, outputPath string) error {
	stdout, stderr, err := runCaptured(ctx, config, "ffmpeg",
		"-y", "-i", inputPath, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", outputPath,
	)
	if err != nil {
		return commandError("ffmpeg source conversion", err, stdout, stderr)
	}
	return nil
}

func runVAD(ctx context.Context, config Config, sourceWAV, workDir string) (string, error) {
	stdout, stderr, err := runCaptured(ctx, config, config.VADBin,
		"-f", sourceWAV,
		"-t", strconv.Itoa(config.Threads),
		"--vad-model", config.VADModel,
		"--vad-threshold", formatFloat(config.VADThreshold),
		"--vad-min-speech-duration-ms", strconv.Itoa(config.VADMinSpeechMS),
		"--vad-min-silence-duration-ms", strconv.Itoa(config.VADMinSilenceMS),
		"--vad-speech-pad-ms", strconv.Itoa(config.VADPadMS),
		"--vad-samples-overlap", formatFloat(config.VADOverlap),
	)
	if err != nil {
		return "", commandError("VAD", err, stdout, stderr)
	}
	parts := make([]string, 0, 2)
	if stdout != "" {
		parts = append(parts, stdout)
	}
	if stderr != "" {
		parts = append(parts, stderr)
	}
	combined := strings.Join(parts, "\n")
	if err := os.WriteFile(filepath.Join(workDir, "vad-output.txt"), []byte(combined), 0o644); err != nil {
		return "", fmt.Errorf("write raw VAD output: %w", err)
	}
	return combined, nil
}

func cutWindow(ctx context.Context, config Config, sourceWAV, outputWAV string, item window) error {
	stdout, stderr, err := runCaptured(ctx, config, "ffmpeg",
		"-y",
		"-ss", fmt.Sprintf("%.3f", float64(item.startMS)/1000),
		"-to", fmt.Sprintf("%.3f", float64(item.endMS)/1000),
		"-i", sourceWAV,
		"-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", outputWAV,
	)
	if err != nil {
		return commandError("ffmpeg window cut", err, stdout, stderr)
	}
	return nil
}

func transcribeWindows(ctx context.Context, config Config, wavPaths []string, workDir string) ([]transcriptionOutput, error) {
	args := []string{
		"-m", config.ModelPath,
		"-l", config.Language,
		"-ovtt",
		"-t", strconv.Itoa(config.Threads),
		"-bs", strconv.Itoa(config.WhisperBeamSize),
		"-bo", strconv.Itoa(config.WhisperBestOf),
	}
	if config.WhisperMaxLen != 0 {
		args = append(args, "-ml", strconv.Itoa(config.WhisperMaxLen), "-sow")
	}
	preset := resolveDTWPreset(config.ModelPath, config.WhisperDTW)
	if preset != "" {
		args = append(args, "-ojf", "-dtw", preset, "-nfa")
		logf("Using DTW token alignment preset %s.", preset)
	}
	if config.WhisperNoFallback {
		args = append(args, "-nf")
	}
	if config.WhisperNoGPU {
		args = append(args, "-ng")
	}
	args = append(args, wavPaths...)

	cmd := newCommand(ctx, config, config.WhisperBin, args...)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logCommand(cmd)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("whisper-cli command failed: %w", err)
	}

	outputs := make([]transcriptionOutput, 0, len(wavPaths))
	for _, wavPath := range wavPaths {
		output := transcriptionOutput{
			vttPath:  wavPath + ".vtt",
			jsonPath: wavPath + ".json",
			hasJSON:  preset != "",
		}
		if err := requireRegularFile(output.vttPath, "window VTT output"); err != nil {
			return nil, err
		}
		if output.hasJSON {
			if err := requireRegularFile(output.jsonPath, "window full JSON output"); err != nil {
				return nil, err
			}
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

func runCaptured(ctx context.Context, config Config, name string, args ...string) (string, string, error) {
	cmd := newCommand(ctx, config, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	logCommand(cmd)
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func newCommand(ctx context.Context, config Config, name string, args ...string) *exec.Cmd {
	if config.LowPriority {
		ionice, ioniceErr := exec.LookPath("ionice")
		nice, niceErr := exec.LookPath("nice")
		if ioniceErr == nil && niceErr == nil {
			wrapped := []string{"-c3", nice, "-n", "10", name}
			wrapped = append(wrapped, args...)
			return exec.CommandContext(ctx, ionice, wrapped...)
		}
	}
	return exec.CommandContext(ctx, name, args...)
}

func logCommand(cmd *exec.Cmd) {
	fmt.Fprintln(os.Stderr, "+", strings.Join(cmd.Args, " "))
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func logCue(label string, item cue) {
	text := make([]string, len(item.textLines))
	for index, line := range item.textLines {
		text[index] = strings.TrimSpace(line)
	}
	logf("%s: %s --> %s | %s", label, msToTimestamp(item.startMS), msToTimestamp(item.endMS), strings.Join(text, " / "))
}

func commandError(label string, err error, stdout, stderr string) error {
	parts := make([]string, 0, 2)
	if value := strings.TrimSpace(stdout); value != "" {
		parts = append(parts, "stdout:\n"+value)
	}
	if value := strings.TrimSpace(stderr); value != "" {
		parts = append(parts, "stderr:\n"+value)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%s failed: %w", label, err)
	}
	return fmt.Errorf("%s failed: %w\n%s", label, err, strings.Join(parts, "\n"))
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

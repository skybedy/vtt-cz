package localwhisper

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	defaultWhisperBin = "~/whisper.cpp/build/bin/whisper-cli"
	defaultVADBin     = "~/whisper.cpp/build/bin/vad-speech-segments"
	defaultModel      = "~/whisper.cpp/models/ggml-large-v3.bin"
	defaultVADModel   = "~/whisper.cpp/models/for-tests-silero-v5.1.2-ggml.bin"
)

// Config contains the local Whisper/VAD pipeline settings. The defaults mirror
// scripts/local_whisper_vad_subtitles.py so the retained Python version can be
// used as a readable reference and parity oracle.
type Config struct {
	InputPath  string
	OutputPath string
	Language   string

	WhisperBin string
	VADBin     string
	ModelPath  string
	VADModel   string

	VADThreshold    float64
	VADMinSpeechMS  int
	VADMinSilenceMS int
	VADPadMS        int
	VADOverlap      float64

	PrePadMS                int
	PostPadMS               int
	LongGapPreRollMS        int
	LongGapThresholdMS      int
	FirstCueLeadMS          int
	FirstCueZeroThresholdMS int
	NoClampFirstCueAfterGap bool
	MergeGapMS              int
	MaxWindowMS             int

	WindowMode     string
	FixedWindowMS  int
	FixedOverlapMS int
	VADLogPath     string

	Threads               int
	WhisperBeamSize       int
	WhisperBestOf         int
	WhisperMaxLen         int
	WhisperDTW            string
	DTWCueLeadMS          int
	DTWCueTailMS          int
	WhisperNoFallback     bool
	WhisperNoGPU          bool
	MaxWindows            int
	KeepMusicCues         bool
	NoDeduplicateOverlaps bool
	KeepTemp              bool
	DryRunVAD             bool
	LowPriority           bool
}

type segment struct {
	startMS int
	endMS   int
}

type window struct {
	startMS        int
	endMS          int
	anchorStartMS  int
	followsLongGap bool
}

type cue struct {
	startMS   int
	endMS     int
	textLines []string
	settings  string
}

type dtwBound struct {
	startMS *int
	endMS   *int
}

// DefaultConfig returns settings equivalent to the Python reference. Callers
// may override the four model/binary locations with VTT_CZ_WHISPER_BIN,
// VTT_CZ_VAD_BIN, VTT_CZ_WHISPER_MODEL and VTT_CZ_VAD_MODEL.
func DefaultConfig(inputPath, outputPath string) Config {
	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}

	return Config{
		InputPath:  inputPath,
		OutputPath: outputPath,
		Language:   "es",

		WhisperBin: configuredPath("VTT_CZ_WHISPER_BIN", defaultWhisperBin),
		VADBin:     configuredPath("VTT_CZ_VAD_BIN", defaultVADBin),
		ModelPath:  configuredPath("VTT_CZ_WHISPER_MODEL", defaultModel),
		VADModel:   configuredPath("VTT_CZ_VAD_MODEL", defaultVADModel),

		VADThreshold:    0.55,
		VADMinSpeechMS:  300,
		VADMinSilenceMS: 700,
		VADPadMS:        250,
		VADOverlap:      0.20,

		PrePadMS:                2000,
		PostPadMS:               500,
		LongGapPreRollMS:        2500,
		LongGapThresholdMS:      5000,
		FirstCueLeadMS:          750,
		FirstCueZeroThresholdMS: 250,
		MergeGapMS:              1200,
		MaxWindowMS:             25000,

		WindowMode:     "vad",
		FixedWindowMS:  30000,
		FixedOverlapMS: 5000,

		Threads:         threads,
		WhisperBeamSize: 5,
		WhisperBestOf:   5,
		WhisperDTW:      "auto",
		DTWCueLeadMS:    200,
		DTWCueTailMS:    500,
		LowPriority:     true,
	}
}

func configuredPath(environmentName, fallback string) string {
	value := os.Getenv(environmentName)
	if value == "" {
		value = fallback
	}
	return expandHome(value)
}

func expandHome(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" || len(path) > 2 && path[:2] == "~/" {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

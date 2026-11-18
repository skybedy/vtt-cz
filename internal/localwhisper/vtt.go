package localwhisper

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var (
	timestampPattern   = regexp.MustCompile(`^(\d{2}:\d{2}:\d{2}\.\d{3})\s+-->\s+(\d{2}:\d{2}:\d{2}\.\d{3})(.*)$`)
	jsonIntegerPattern = regexp.MustCompile(`^-?(?:0|[1-9]\d*)$`)
	foldCaser          = cases.Fold()
)

func timestampToMS(value string) (int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}
	secondsParts := strings.Split(parts[2], ".")
	if len(secondsParts) != 2 {
		return 0, fmt.Errorf("invalid VTT timestamp %q", value)
	}

	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q: %w", value, err)
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q: %w", value, err)
	}
	seconds, err := strconv.Atoi(secondsParts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q: %w", value, err)
	}
	millis, err := strconv.Atoi(secondsParts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid VTT timestamp %q: %w", value, err)
	}
	return hours*3_600_000 + minutes*60_000 + seconds*1000 + millis, nil
}

func msToTimestamp(ms int) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3_600_000
	ms %= 3_600_000
	minutes := ms / 60_000
	ms %= 60_000
	seconds := ms / 1000
	millis := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

func parseVTT(path string) ([]cue, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	text := strings.TrimPrefix(string(contents), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	var cues []cue
	for i := 0; i < len(lines); {
		match := timestampPattern.FindStringSubmatch(lines[i])
		if match == nil && i+1 < len(lines) {
			match = timestampPattern.FindStringSubmatch(lines[i+1])
			if match != nil {
				i++
			}
		}
		if match == nil {
			i++
			continue
		}

		startMS, err := timestampToMS(match[1])
		if err != nil {
			return nil, err
		}
		endMS, err := timestampToMS(match[2])
		if err != nil {
			return nil, err
		}
		i++
		var textLines []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			textLines = append(textLines, lines[i])
			i++
		}
		if len(textLines) > 0 && endMS > startMS {
			cues = append(cues, cue{
				startMS:   startMS,
				endMS:     endMS,
				textLines: textLines,
				settings:  match[3],
			})
		}
		i++
	}
	return cues, nil
}

func writeVTT(path string, cues []cue) error {
	lines := []string{"WEBVTT", ""}
	for _, current := range cues {
		settings := strings.TrimRightFunc(current.settings, unicode.IsSpace)
		lines = append(lines, fmt.Sprintf(
			"%s --> %s%s",
			msToTimestamp(current.startMS),
			msToTimestamp(current.endMS),
			settings,
		))
		lines = append(lines, current.textLines...)
		lines = append(lines, "")
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func shiftCues(cues []cue, offsetMS int) []cue {
	shifted := make([]cue, 0, len(cues))
	for _, current := range cues {
		current.startMS += offsetMS
		current.endMS += offsetMS
		shifted = append(shifted, current)
	}
	return shifted
}

func resolveDTWPreset(modelPath, requested string) string {
	if requested == "off" {
		return ""
	}
	if requested != "auto" {
		return requested
	}

	name := strings.ReplaceAll(strings.ToLower(filepath.Base(modelPath)), "_", "-")
	presets := []struct {
		marker string
		preset string
	}{
		{"large-v3-turbo", "large.v3.turbo"},
		{"large-v3", "large.v3"},
		{"large-v2", "large.v2"},
		{"large-v1", "large.v1"},
		{"medium.en", "medium.en"},
		{"medium", "medium"},
		{"small.en", "small.en"},
		{"small", "small"},
		{"base.en", "base.en"},
		{"base", "base"},
		{"tiny.en", "tiny.en"},
		{"tiny", "tiny"},
	}
	for _, candidate := range presets {
		if strings.Contains(name, candidate.marker) {
			return candidate.preset
		}
	}
	logf("Could not infer a DTW preset from model name %s; DTW alignment is disabled.", filepath.Base(modelPath))
	return ""
}

func parseDTWBounds(path string, expectedCount int) ([]dtwBound, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		logf("Warning: could not read DTW JSON %s: %v", path, err)
		return nil, false
	}
	if !utf8.Valid(contents) {
		logf("Warning: repairing invalid UTF-8 in DTW JSON %s.", path)
	}

	var payload struct {
		Transcription json.RawMessage `json:"transcription"`
	}
	if err := json.Unmarshal(contents, &payload); err != nil {
		logf("Warning: could not parse DTW JSON %s: %v", path, err)
		return nil, false
	}
	trimmedTranscription := strings.TrimSpace(string(payload.Transcription))
	if !strings.HasPrefix(trimmedTranscription, "[") {
		return nil, false
	}
	var transcription []json.RawMessage
	if err := json.Unmarshal(payload.Transcription, &transcription); err != nil {
		logf("Warning: could not parse DTW transcription in %s: %v", path, err)
		return nil, false
	}
	if len(transcription) != expectedCount {
		logf("Warning: DTW/VTT cue count mismatch for %s: expected %d, got %d; using fallback timing.", path, expectedCount, len(transcription))
		return nil, false
	}

	bounds := make([]dtwBound, 0, len(transcription))
	for _, rawSegment := range transcription {
		var segment struct {
			Tokens json.RawMessage `json:"tokens"`
		}
		if err := json.Unmarshal(rawSegment, &segment); err != nil {
			bounds = append(bounds, dtwBound{})
			continue
		}

		var tokens []json.RawMessage
		trimmedTokens := strings.TrimSpace(string(segment.Tokens))
		if strings.HasPrefix(trimmedTokens, "[") {
			_ = json.Unmarshal(segment.Tokens, &tokens)
		}
		var times []int
		for _, rawToken := range tokens {
			var token struct {
				Text json.RawMessage `json:"text"`
				Time json.RawMessage `json:"t_dtw"`
			}
			if err := json.Unmarshal(rawToken, &token); err != nil {
				continue
			}
			timeText := strings.TrimSpace(string(token.Time))
			if !jsonIntegerPattern.MatchString(timeText) {
				continue
			}
			ticks, err := strconv.Atoi(timeText)
			if err != nil || ticks < 0 {
				continue
			}
			text := jsonValueString(token.Text)
			if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "[_") {
				continue
			}
			times = append(times, ticks*10)
		}

		if len(times) == 0 {
			bounds = append(bounds, dtwBound{})
			continue
		}
		startMS, endMS := times[0], times[len(times)-1]
		bounds = append(bounds, dtwBound{startMS: &startMS, endMS: &endMS})
	}
	return bounds, true
}

func jsonValueString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func alignCueBoundsFromDTW(cues []cue, bounds []dtwBound, currentWindow window, leadMS, tailMS int) ([]cue, error) {
	if len(cues) != len(bounds) {
		return nil, fmt.Errorf("DTW/VTT cue count mismatch: got %d cues and %d bounds", len(cues), len(bounds))
	}

	aligned := make([]cue, 0, len(cues))
	changedStarts := 0
	changedEnds := 0
	for index, current := range cues {
		startMS := current.startMS
		endMS := current.endMS
		if bounds[index].startMS != nil {
			candidate := maxInt(currentWindow.startMS, currentWindow.startMS+*bounds[index].startMS-leadMS)
			if candidate < current.endMS {
				if candidate != startMS {
					changedStarts++
				}
				startMS = candidate
			}
		}
		if bounds[index].endMS != nil {
			candidate := minInt(currentWindow.endMS, currentWindow.startMS+*bounds[index].endMS+tailMS)
			if candidate > startMS && candidate < endMS {
				endMS = candidate
				changedEnds++
			}
		}
		current.startMS = startMS
		current.endMS = endMS
		aligned = append(aligned, current)
	}

	for index := 1; index < len(aligned); index++ {
		previous := &aligned[index-1]
		current := aligned[index]
		if previous.endMS > current.startMS && current.startMS > previous.startMS {
			previous.endMS = current.startMS
		}
	}
	logf("DTW-aligned %d cue start(s) and %d cue end(s) in this window.", changedStarts, changedEnds)
	return aligned, nil
}

func shiftAndGuardCues(cues []cue, currentWindow window, config Config, bounds []dtwBound, hasDTW bool) ([]cue, error) {
	shifted := shiftCues(cues, currentWindow.startMS)
	if hasDTW {
		return alignCueBoundsFromDTW(
			shifted,
			bounds,
			currentWindow,
			maxInt(0, config.DTWCueLeadMS),
			maxInt(0, config.DTWCueTailMS),
		)
	}
	if len(shifted) == 0 || !currentWindow.followsLongGap || config.NoClampFirstCueAfterGap {
		return shifted, nil
	}
	if shifted[0].startMS-currentWindow.startMS > config.FirstCueZeroThresholdMS {
		return shifted, nil
	}

	minimumStartMS := maxInt(0, currentWindow.anchorStartMS-config.FirstCueLeadMS)
	if shifted[0].startMS < minimumStartMS && shifted[0].endMS > minimumStartMS {
		logf("Clamping first cue after long gap from %s to %s.", msToTimestamp(shifted[0].startMS), msToTimestamp(minimumStartMS))
		shifted[0].startMS = minimumStartMS
	}
	return shifted, nil
}

func isNonSpeechCue(current cue) bool {
	text := normalizeFold(strings.Join(current.textLines, " "))
	words := asciiWords(text)
	if len(words) == 0 {
		return false
	}

	stripped := strings.TrimSpace(text)
	wrapped := strings.HasPrefix(stripped, "(") && strings.HasSuffix(stripped, ")") ||
		strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]") ||
		strings.HasPrefix(stripped, "♪") || strings.HasPrefix(stripped, "♫")
	if !wrapped {
		return false
	}

	soundWords := map[string]struct{}{
		"music": {}, "musica": {}, "dramatic": {}, "upbeat": {},
		"ominous": {}, "tense": {}, "somber": {}, "soft": {},
		"loud": {}, "applause": {}, "laughter": {}, "laughing": {},
		"alarm": {}, "alarms": {}, "explosion": {}, "sirens": {}, "siren": {},
	}
	for _, word := range words {
		if _, ok := soundWords[word]; ok {
			return true
		}
	}
	return false
}

func normalizeFold(text string) string {
	decomposed := norm.NFKD.String(foldCaser.String(text))
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, decomposed)
}

func asciiWords(text string) []string {
	var words []string
	for start := -1; ; {
		for start < 0 {
			if len(text) == 0 {
				return words
			}
			if text[0] >= 'a' && text[0] <= 'z' {
				start = 0
				break
			}
			_, size := utf8.DecodeRuneInString(text)
			text = text[size:]
		}

		end := start
		for end < len(text) && text[end] >= 'a' && text[end] <= 'z' {
			end++
		}
		words = append(words, text[start:end])
		text = text[end:]
		start = -1
	}
}

type normalizedWordSpan struct {
	value string
	start int
	end   int
}

func normalizedWordSpans(text string) []normalizedWordSpan {
	var spans []normalizedWordSpan
	start := -1
	for index, r := range text {
		isWord := r != '_' && (unicode.IsLetter(r) || unicode.IsNumber(r))
		if isWord && start < 0 {
			start = index
		}
		if !isWord && start >= 0 {
			spans = append(spans, normalizedWordSpan{
				value: normalizeFold(text[start:index]),
				start: start,
				end:   index,
			})
			start = -1
		}
	}
	if start >= 0 {
		spans = append(spans, normalizedWordSpan{
			value: normalizeFold(text[start:]),
			start: start,
			end:   len(text),
		})
	}
	return spans
}

func cueWordValues(current cue) []string {
	lines := make([]string, 0, len(current.textLines))
	for _, line := range current.textLines {
		lines = append(lines, strings.TrimSpace(line))
	}
	spans := normalizedWordSpans(strings.Join(lines, " "))
	values := make([]string, 0, len(spans))
	for _, span := range spans {
		values = append(values, span.value)
	}
	return values
}

func containsWordSequence(container, candidate cue) bool {
	containerWords := cueWordValues(container)
	candidateWords := cueWordValues(candidate)
	if len(candidateWords) == 0 || len(candidateWords) > len(containerWords) {
		return false
	}
	for start := 0; start+len(candidateWords) <= len(containerWords); start++ {
		if equalStrings(containerWords[start:start+len(candidateWords)], candidateWords) {
			return true
		}
	}
	return false
}

func trimRepeatedPrefixFromPrevious(previous, current cue, minWords, maxTrailingWords int) (cue, bool, bool) {
	if previous.endMS <= current.startMS {
		return previous, true, false
	}

	previousText := joinedCueText(previous)
	currentText := joinedCueText(current)
	previousWords := normalizedWordSpans(previousText)
	currentWords := normalizedWordSpans(currentText)
	if len(previousWords) < minWords || len(currentWords) < minWords {
		return previous, true, false
	}

	bestStart := -1
	bestLength := 0
	for start := range previousWords {
		maximumLength := minInt(len(previousWords)-start, len(currentWords))
		length := 0
		for length < maximumLength && previousWords[start+length].value == currentWords[length].value {
			length++
		}
		trailingWords := len(previousWords) - (start + length)
		if length >= minWords && trailingWords <= maxTrailingWords && length > bestLength {
			bestStart = start
			bestLength = length
		}
	}
	if bestStart < 0 {
		return previous, true, false
	}

	remaining := strings.TrimRight(previousText[:previousWords[bestStart].start], " \t,;:-")
	if remaining == "" {
		return cue{}, false, true
	}
	endMS := minInt(previous.endMS, current.startMS)
	if endMS <= previous.startMS {
		return cue{}, false, true
	}
	previous.endMS = endMS
	previous.textLines = []string{remaining}
	return previous, true, true
}

func joinedCueText(current cue) string {
	lines := make([]string, 0, len(current.textLines))
	for _, line := range current.textLines {
		lines = append(lines, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

func deduplicateOverlappingCues(cues []cue) ([]cue, int) {
	if len(cues) < 2 {
		return cues, 0
	}
	result := make([]cue, 0, len(cues))
	removed := 0
	for _, current := range cues {
		if len(result) > 0 {
			trimmed, keep, changed := trimRepeatedPrefixFromPrevious(result[len(result)-1], current, 4, 3)
			if changed {
				removed++
				if keep {
					result[len(result)-1] = trimmed
				} else {
					result = result[:len(result)-1]
				}
			}
		}
		result = append(result, current)
	}
	return result, removed
}

func dropAdjacentRepeatedFragments(cues []cue) ([]cue, int) {
	if len(cues) < 2 {
		return cues, 0
	}
	result := make([]cue, 0, len(cues))
	dropped := 0
	for _, current := range cues {
		if len(result) > 0 {
			previous := result[len(result)-1]
			previousWords := cueWordValues(previous)
			currentWords := cueWordValues(current)
			gapMS := current.startMS - previous.endMS
			if len(currentWords) > 0 && len(currentWords) <= 4 &&
				len(previousWords) >= len(currentWords) &&
				equalStrings(previousWords[len(previousWords)-len(currentWords):], currentWords) &&
				gapMS >= -250 && gapMS <= 750 {
				dropped++
				continue
			}
		}
		result = append(result, current)
	}
	return result, dropped
}

func resolveOverlappingCues(cues []cue, shortNestedMS int) ([]cue, int, int, error) {
	if len(cues) < 2 {
		return cues, 0, 0, nil
	}
	result := make([]cue, 0, len(cues))
	droppedNested := 0
	shortenedPrevious := 0
	for _, current := range cues {
		if len(result) == 0 {
			result = append(result, current)
			continue
		}

		previous := result[len(result)-1]
		if current.startMS >= previous.endMS {
			result = append(result, current)
			continue
		}

		fullyNested := current.endMS <= previous.endMS
		currentDurationMS := current.endMS - current.startMS
		if fullyNested && (containsWordSequence(previous, current) || currentDurationMS <= shortNestedMS) {
			droppedNested++
			continue
		}

		if current.startMS == previous.startMS {
			previousWords := cueWordValues(previous)
			currentWords := cueWordValues(current)
			if containsWordSequence(current, previous) ||
				current.endMS >= previous.endMS && len(currentWords) > len(previousWords) {
				result[len(result)-1] = current
			}
			droppedNested++
			continue
		}

		if current.startMS < previous.startMS {
			return nil, 0, 0, fmt.Errorf(
				"cannot safely resolve out-of-order overlapping cues beginning at %s and %s",
				msToTimestamp(previous.startMS),
				msToTimestamp(current.startMS),
			)
		}

		previous.endMS = current.startMS
		result[len(result)-1] = previous
		shortenedPrevious++
		result = append(result, current)
	}
	return result, droppedNested, shortenedPrevious, nil
}

func validateCues(cues []cue, durationMS int) error {
	if len(cues) == 0 {
		return errors.New("no subtitle cues were produced")
	}
	latest := cues[0].endMS
	for _, current := range cues[1:] {
		if current.endMS > latest {
			latest = current.endMS
		}
	}
	if latest > durationMS+1000 {
		return fmt.Errorf(
			"subtitle end time %s exceeds source duration %s",
			msToTimestamp(latest),
			msToTimestamp(durationMS),
		)
	}
	for index := 1; index < len(cues); index++ {
		previous, current := cues[index-1], cues[index]
		if current.startMS < previous.endMS {
			return fmt.Errorf(
				"overlapping subtitle cues remain after cleanup: %s --> %s and %s --> %s",
				msToTimestamp(previous.startMS),
				msToTimestamp(previous.endMS),
				msToTimestamp(current.startMS),
				msToTimestamp(current.endMS),
			)
		}
	}
	return nil
}

func sortCues(cues []cue) {
	sort.SliceStable(cues, func(i, j int) bool {
		if cues[i].startMS != cues[j].startMS {
			return cues[i].startMS < cues[j].startMS
		}
		if cues[i].endMS != cues[j].endMS {
			return cues[i].endMS < cues[j].endMS
		}
		return compareStringSlices(cues[i].textLines, cues[j].textLines) < 0
	})
}

func compareStringSlices(left, right []string) int {
	limit := minInt(len(left), len(right))
	for index := 0; index < limit; index++ {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

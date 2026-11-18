package localwhisper

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type vadCandidate struct {
	start float64
	end   float64
}

var (
	vadPairPattern = regexp.MustCompile(`(?i)start\s*=\s*([-+]?\d+(?:\.\d+)?).*?end\s*=\s*([-+]?\d+(?:\.\d+)?)`)
	numberPattern  = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)
)

// parseVADCandidates extracts the start/end pairs printed by
// vad-speech-segments. Tagged output takes precedence over the two-column
// fallback, matching the retained Python implementation.
func parseVADCandidates(output string) []vadCandidate {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	candidates := make([]vadCandidate, 0)

	for _, line := range lines {
		match := vadPairPattern.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if match == nil {
			continue
		}
		start, startErr := strconv.ParseFloat(match[1], 64)
		end, endErr := strconv.ParseFloat(match[2], 64)
		if startErr == nil && endErr == nil && end > start {
			candidates = append(candidates, vadCandidate{start: start, end: end})
		}
	}
	if len(candidates) > 0 {
		return candidates
	}

	for _, line := range lines {
		numbers := numberPattern.FindAllString(strings.TrimSuffix(line, "\r"), -1)
		if len(numbers) != 2 {
			continue
		}
		start, startErr := strconv.ParseFloat(numbers[0], 64)
		end, endErr := strconv.ParseFloat(numbers[1], 64)
		if startErr == nil && endErr == nil && end > start {
			candidates = append(candidates, vadCandidate{start: start, end: end})
		}
	}
	return candidates
}

// convertVADUnits infers whether VAD emitted seconds, centiseconds, or
// milliseconds and converts the candidates into clamped millisecond segments.
func convertVADUnits(candidates []vadCandidate, durationMS int) []segment {
	if len(candidates) == 0 {
		return nil
	}

	maxEnd := candidates[0].end
	for _, candidate := range candidates[1:] {
		if candidate.end > maxEnd {
			maxEnd = candidate.end
		}
	}

	durationSeconds := float64(durationMS) / 1000
	scales := [...]float64{1, 0.01, 0.001}
	scale := 0.0
	for _, candidateScale := range scales {
		if maxEnd*candidateScale <= durationSeconds*1.10 {
			scale = candidateScale
			break
		}
	}
	if scale == 0 {
		scale = scales[0]
		bestDistance := math.Abs(maxEnd*scale - durationSeconds)
		for _, candidateScale := range scales[1:] {
			distance := math.Abs(maxEnd*candidateScale - durationSeconds)
			if distance < bestDistance {
				scale = candidateScale
				bestDistance = distance
			}
		}
	}

	segments := make([]segment, 0, len(candidates))
	for _, candidate := range candidates {
		startMS := maxInt(0, int(math.RoundToEven(candidate.start*scale*1000)))
		endMS := minInt(durationMS, int(math.RoundToEven(candidate.end*scale*1000)))
		if endMS > startMS {
			segments = append(segments, segment{startMS: startMS, endMS: endMS})
		}
	}
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].startMS != segments[j].startMS {
			return segments[i].startMS < segments[j].startMS
		}
		return segments[i].endMS < segments[j].endMS
	})
	return segments
}

func mergeSegments(
	segments []segment,
	durationMS int,
	prePadMS int,
	postPadMS int,
	longGapPreRollMS int,
	longGapThresholdMS int,
	mergeGapMS int,
	maxWindowMS int,
) []window {
	padded := make([]window, 0, len(segments))
	previousEndMS := 0
	for _, current := range segments {
		extraPreRollMS := 0
		followsLongGap := current.startMS-previousEndMS >= longGapThresholdMS
		if followsLongGap {
			extraPreRollMS = longGapPreRollMS
		}
		padded = append(padded, window{
			startMS:        maxInt(0, current.startMS-prePadMS-extraPreRollMS),
			endMS:          minInt(durationMS, current.endMS+postPadMS),
			anchorStartMS:  current.startMS,
			followsLongGap: followsLongGap,
		})
		previousEndMS = maxInt(previousEndMS, current.endMS)
	}
	if len(padded) == 0 {
		return nil
	}

	windows := make([]window, 0, len(padded))
	current := padded[0]
	for _, next := range padded[1:] {
		gapMS := next.startMS - current.endMS
		mergedLengthMS := next.endMS - current.startMS
		if gapMS <= mergeGapMS && mergedLengthMS <= maxWindowMS {
			current.endMS = maxInt(current.endMS, next.endMS)
			continue
		}
		windows = append(windows, current)
		current = next
	}
	windows = append(windows, current)
	return splitLongWindows(windows, maxWindowMS)
}

func splitLongWindows(windows []window, maxWindowMS int) []window {
	result := make([]window, 0, len(windows))
	for _, current := range windows {
		lengthMS := current.endMS - current.startMS
		if lengthMS <= maxWindowMS {
			result = append(result, current)
			continue
		}

		parts := maxInt(1, int(math.Ceil(float64(lengthMS)/float64(maxWindowMS))))
		partLengthMS := int(math.Ceil(float64(lengthMS) / float64(parts)))
		startMS := current.startMS
		firstPart := true
		for startMS < current.endMS {
			endMS := minInt(current.endMS, startMS+partLengthMS)
			if endMS > startMS {
				part := window{
					startMS:       startMS,
					endMS:         endMS,
					anchorStartMS: startMS,
				}
				if firstPart {
					part.anchorStartMS = current.anchorStartMS
					part.followsLongGap = current.followsLongGap
				}
				result = append(result, part)
			}
			startMS = endMS
			firstPart = false
		}
	}
	return result
}

func buildFixedWindows(durationMS, windowMS, overlapMS int) ([]window, error) {
	if windowMS <= 0 {
		return nil, fmt.Errorf("fixed-window length must be greater than zero")
	}
	if overlapMS >= windowMS {
		return nil, fmt.Errorf("fixed-window overlap must be smaller than the window length")
	}

	windows := make([]window, 0)
	stepMS := windowMS - overlapMS
	for startMS := 0; startMS < durationMS; startMS += stepMS {
		endMS := minInt(durationMS, startMS+windowMS)
		windows = append(windows, window{
			startMS:       startMS,
			endMS:         endMS,
			anchorStartMS: startMS,
		})
		if endMS == durationMS {
			break
		}
	}
	return windows, nil
}

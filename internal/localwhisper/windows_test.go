package localwhisper

import (
	"reflect"
	"testing"
)

func TestParseVADCandidatesPrefersTaggedOutput(t *testing.T) {
	output := "noise 4 5\r\n" +
		"speech START = 1.25, probability = 0.9, END = 2.75\n" +
		"start=8 end=7\n" +
		"10 11\n"

	want := []vadCandidate{{start: 1.25, end: 2.75}}
	if got := parseVADCandidates(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseVADCandidates() = %#v, want %#v", got, want)
	}
}

func TestParseVADCandidatesFallsBackToTwoColumns(t *testing.T) {
	output := "header\n-1.5 2.25\n8 7\n1 2 3\n+4.0 +5.5\n"
	want := []vadCandidate{
		{start: -1.5, end: 2.25},
		{start: 4, end: 5.5},
	}
	if got := parseVADCandidates(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseVADCandidates() = %#v, want %#v", got, want)
	}
}

func TestConvertVADUnits(t *testing.T) {
	tests := []struct {
		name       string
		candidates []vadCandidate
		durationMS int
		want       []segment
	}{
		{
			name:       "seconds",
			candidates: []vadCandidate{{start: 0.5, end: 2}, {start: 4, end: 5.25}},
			durationMS: 10_000,
			want:       []segment{{startMS: 500, endMS: 2000}, {startMS: 4000, endMS: 5250}},
		},
		{
			name:       "centiseconds",
			candidates: []vadCandidate{{start: 100, end: 250}},
			durationMS: 10_000,
			want:       []segment{{startMS: 1000, endMS: 2500}},
		},
		{
			name:       "milliseconds",
			candidates: []vadCandidate{{start: 1000, end: 2500}},
			durationMS: 10_000,
			want:       []segment{{startMS: 1000, endMS: 2500}},
		},
		{
			name: "clamps sorts and drops empty",
			candidates: []vadCandidate{
				{start: 4900, end: 6000},
				{start: 1000, end: 1000},
				{start: -500, end: 250},
			},
			durationMS: 5000,
			want:       []segment{{startMS: 0, endMS: 250}, {startMS: 4900, endMS: 5000}},
		},
		{
			name:       "python round to even",
			candidates: []vadCandidate{{start: 0.0005, end: 0.0015}, {start: 0.0025, end: 0.0035}},
			durationMS: 1000,
			want:       []segment{{startMS: 0, endMS: 2}, {startMS: 2, endMS: 4}},
		},
		{
			name:       "nearest scale when none fits",
			candidates: []vadCandidate{{start: 100, end: 2000}},
			durationMS: 1000,
			want:       []segment{{startMS: 100, endMS: 1000}},
		},
		{
			name:       "empty",
			candidates: nil,
			durationMS: 1000,
			want:       nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := convertVADUnits(test.candidates, test.durationMS); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("convertVADUnits() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMergeSegments(t *testing.T) {
	segments := []segment{
		{startMS: 1000, endMS: 2000},
		{startMS: 2500, endMS: 3500},
		{startMS: 10_000, endMS: 11_000},
	}
	want := []window{
		{startMS: 0, endMS: 4000, anchorStartMS: 1000},
		{startMS: 7000, endMS: 11_500, anchorStartMS: 10_000, followsLongGap: true},
	}
	got := mergeSegments(segments, 30_000, 1000, 500, 2000, 5000, 1200, 8000)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeSegments() = %#v, want %#v", got, want)
	}
}

func TestMergeSegmentsUsesPreviousRawEndForLongGap(t *testing.T) {
	segments := []segment{
		{startMS: 1000, endMS: 2000},
		{startMS: 7000, endMS: 7500},
	}
	want := []window{
		{startMS: 1000, endMS: 2000, anchorStartMS: 1000},
		{startMS: 6000, endMS: 7500, anchorStartMS: 7000, followsLongGap: true},
	}
	got := mergeSegments(segments, 10_000, 0, 0, 1000, 5000, 0, 10_000)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeSegments() = %#v, want %#v", got, want)
	}
}

func TestSplitLongWindowsPreservesMetadataOnlyOnFirstPart(t *testing.T) {
	windows := []window{{
		startMS:        0,
		endMS:          26_000,
		anchorStartMS:  1234,
		followsLongGap: true,
	}}
	want := []window{
		{startMS: 0, endMS: 8667, anchorStartMS: 1234, followsLongGap: true},
		{startMS: 8667, endMS: 17_334, anchorStartMS: 8667},
		{startMS: 17_334, endMS: 26_000, anchorStartMS: 17_334},
	}
	if got := splitLongWindows(windows, 10_000); !reflect.DeepEqual(got, want) {
		t.Fatalf("splitLongWindows() = %#v, want %#v", got, want)
	}
}

func TestBuildFixedWindows(t *testing.T) {
	got, err := buildFixedWindows(60_000, 30_000, 5000)
	if err != nil {
		t.Fatalf("buildFixedWindows() error = %v", err)
	}
	want := []window{
		{startMS: 0, endMS: 30_000, anchorStartMS: 0},
		{startMS: 25_000, endMS: 55_000, anchorStartMS: 25_000},
		{startMS: 50_000, endMS: 60_000, anchorStartMS: 50_000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildFixedWindows() = %#v, want %#v", got, want)
	}
}

func TestBuildFixedWindowsRejectsInvalidSizeOrOverlap(t *testing.T) {
	for _, test := range []struct {
		name      string
		windowMS  int
		overlapMS int
	}{
		{name: "zero window", windowMS: 0},
		{name: "equal overlap", windowMS: 1000, overlapMS: 1000},
		{name: "larger overlap", windowMS: 1000, overlapMS: 1001},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildFixedWindows(10_000, test.windowMS, test.overlapMS); err == nil {
				t.Fatal("buildFixedWindows() error = nil, want an error")
			}
		})
	}
}

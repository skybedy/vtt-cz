package localwhisper

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAndWriteVTTParity(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "input.vtt")
	contents := "\ufeffWEBVTT\r\n\r\n" +
		"1\r\n" +
		"00:00:01.250 --> 00:00:03.500 align:start position:10%\r\n" +
		"Hello\r\nworld\r\n\r\n" +
		"00:00:04.000 --> 00:00:04.000\r\ndrop zero duration\r\n\r\n" +
		"00:00:05.000 --> 00:00:06.000\r\nFinal\r\n"
	if err := os.WriteFile(inputPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseVTT(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []cue{
		{startMS: 1250, endMS: 3500, textLines: []string{"Hello", "world"}, settings: " align:start position:10%"},
		{startMS: 5000, endMS: 6000, textLines: []string{"Final"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseVTT() = %#v, want %#v", got, want)
	}

	outputPath := filepath.Join(t.TempDir(), "output.vtt")
	got[0].settings += " \t"
	if err := writeVTT(outputPath, got); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	wantOutput := "WEBVTT\n\n" +
		"00:00:01.250 --> 00:00:03.500 align:start position:10%\n" +
		"Hello\nworld\n\n" +
		"00:00:05.000 --> 00:00:06.000\nFinal\n"
	if string(written) != wantOutput {
		t.Fatalf("writeVTT() produced:\n%q\nwant:\n%q", written, wantOutput)
	}
}

func TestTimestampAndShiftParity(t *testing.T) {
	got, err := timestampToMS("01:02:03.004")
	if err != nil {
		t.Fatal(err)
	}
	if got != 3_723_004 || msToTimestamp(got) != "01:02:03.004" || msToTimestamp(-1) != "00:00:00.000" {
		t.Fatalf("timestamp conversion mismatch: got %d / %q / %q", got, msToTimestamp(got), msToTimestamp(-1))
	}

	shifted := shiftCues([]cue{{startMS: 1250, endMS: 3500, textLines: []string{"Hello"}}}, 2000)
	want := []cue{{startMS: 3250, endMS: 5500, textLines: []string{"Hello"}}}
	if !reflect.DeepEqual(shifted, want) {
		t.Fatalf("shiftCues() = %#v, want %#v", shifted, want)
	}
}

func TestResolveDTWPresetParity(t *testing.T) {
	tests := []struct {
		model     string
		requested string
		want      string
	}{
		{"/models/ggml-large-v3.bin", "auto", "large.v3"},
		{"/models/ggml-large-v3-turbo-q5_0.bin", "auto", "large.v3.turbo"},
		{"/models/ggml-medium.en.bin", "auto", "medium.en"},
		{"/models/custom.bin", "auto", ""},
		{"/models/custom.bin", "off", ""},
		{"/models/custom.bin", "small", "small"},
	}
	for _, test := range tests {
		if got := resolveDTWPreset(test.model, test.requested); got != test.want {
			t.Errorf("resolveDTWPreset(%q, %q) = %q, want %q", test.model, test.requested, got, test.want)
		}
	}
}

func TestParseDTWBoundsParity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.wav.json")
	contents := `{"transcription":[` +
		`{"tokens":[` +
		`{"text":"[_BEG_]","t_dtw":0},` +
		`{"text":" Hello","t_dtw":120},` +
		`{"text":" ","t_dtw":130},` +
		`{"text":"float","t_dtw":180.0},` +
		`{"text":"world","t_dtw":250},` +
		`{"text":"negative","t_dtw":-1}` +
		`]},` +
		`{"tokens":[{"text":"[_BEG_]","t_dtw":0}]}` +
		`]}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	bounds, ok := parseDTWBounds(path, 2)
	if !ok || len(bounds) != 2 {
		t.Fatalf("parseDTWBounds() = %#v, %v", bounds, ok)
	}
	if bounds[0].startMS == nil || *bounds[0].startMS != 1200 || bounds[0].endMS == nil || *bounds[0].endMS != 2500 {
		t.Fatalf("first DTW bound = %#v", bounds[0])
	}
	if bounds[1].startMS != nil || bounds[1].endMS != nil {
		t.Fatalf("second DTW bound = %#v, want nil bounds", bounds[1])
	}
	if _, ok := parseDTWBounds(path, 1); ok {
		t.Fatal("cue-count mismatch unexpectedly accepted")
	}
}

func TestShiftAndDTWAlignParity(t *testing.T) {
	start1, end1 := 1000, 2000
	start2, end2 := 4500, 6000
	input := []cue{
		{startMS: 0, endMS: 5000, textLines: []string{"alpha"}},
		{startMS: 4000, endMS: 9000, textLines: []string{"beta"}},
	}
	currentWindow := window{startMS: 10000, endMS: 20000, anchorStartMS: 10000}
	config := Config{DTWCueLeadMS: 200, DTWCueTailMS: 500}
	got, err := shiftAndGuardCues(input, currentWindow, config, []dtwBound{
		{startMS: &start1, endMS: &end1},
		{startMS: &start2, endMS: &end2},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []cue{
		{startMS: 10800, endMS: 12500, textLines: []string{"alpha"}},
		{startMS: 14300, endMS: 16500, textLines: []string{"beta"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shiftAndGuardCues(DTW) = %#v, want %#v", got, want)
	}

	longGapWindow := window{startMS: 5000, endMS: 10000, anchorStartMS: 8000, followsLongGap: true}
	config = Config{FirstCueLeadMS: 750, FirstCueZeroThresholdMS: 250}
	got, err = shiftAndGuardCues(
		[]cue{{startMS: 0, endMS: 4000, textLines: []string{"hello"}}},
		longGapWindow,
		config,
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	want = []cue{{startMS: 7250, endMS: 9000, textLines: []string{"hello"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shiftAndGuardCues(fallback) = %#v, want %#v", got, want)
	}
}

func TestIsNonSpeechCueParity(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"(Music)", true},
		{"[Música]", true},
		{"♪ ominous strings", true},
		{"(soft music continues)", true},
		{"(Laughter)", true},
		{"Music", false},
		{"[Door closes]", false},
		{"(Musical interlude)", false},
		{"♫", false},
		{"[Hudba]", false},
		{"♪ Spoken words", false},
	}
	for _, test := range tests {
		if got := isNonSpeechCue(cue{textLines: []string{test.text}}); got != test.want {
			t.Errorf("isNonSpeechCue(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestDeduplicateAndDropFragmentsParity(t *testing.T) {
	input := []cue{
		{startMS: 0, endMS: 5000, textLines: []string{"We came here to find the old house today"}},
		{startMS: 4000, endMS: 7000, textLines: []string{"FÍND the old house today and leave"}},
	}
	got, removed := deduplicateOverlappingCues(input)
	want := []cue{
		{startMS: 0, endMS: 4000, textLines: []string{"We came here to"}},
		input[1],
	}
	if removed != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("deduplicateOverlappingCues() = %#v, %d; want %#v, 1", got, removed, want)
	}

	fullyRepeated := []cue{
		{startMS: 0, endMS: 5000, textLines: []string{"The old house is empty"}},
		{startMS: 4000, endMS: 7000, textLines: []string{"The old house is empty now"}},
	}
	got, removed = deduplicateOverlappingCues(fullyRepeated)
	if removed != 1 || !reflect.DeepEqual(got, fullyRepeated[1:]) {
		t.Fatalf("full repeated cue was not removed: %#v, %d", got, removed)
	}

	fragments := []cue{
		{startMS: 0, endMS: 2000, textLines: []string{"I really like this house"}},
		{startMS: 2200, endMS: 3000, textLines: []string{"This house"}},
		{startMS: 4000, endMS: 5000, textLines: []string{"house"}},
	}
	got, removed = dropAdjacentRepeatedFragments(fragments)
	want = []cue{fragments[0], fragments[2]}
	if removed != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("dropAdjacentRepeatedFragments() = %#v, %d; want %#v, 1", got, removed, want)
	}
}

func TestResolveOverlappingCuesParity(t *testing.T) {
	input := []cue{
		{startMS: 0, endMS: 5000, textLines: []string{"This is a long subtitle"}},
		{startMS: 1000, endMS: 1500, textLines: []string{"noise"}},
		{startMS: 3000, endMS: 6000, textLines: []string{"Different sentence"}},
	}
	got, dropped, shortened, err := resolveOverlappingCues(input, 750)
	if err != nil {
		t.Fatal(err)
	}
	want := []cue{
		{startMS: 0, endMS: 3000, textLines: []string{"This is a long subtitle"}},
		{startMS: 3000, endMS: 6000, textLines: []string{"Different sentence"}},
	}
	if dropped != 1 || shortened != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveOverlappingCues() = %#v, %d, %d; want %#v, 1, 1", got, dropped, shortened, want)
	}

	sameStart := []cue{
		{startMS: 0, endMS: 3000, textLines: []string{"hello world"}},
		{startMS: 0, endMS: 4000, textLines: []string{"hello world again"}},
	}
	got, dropped, shortened, err = resolveOverlappingCues(sameStart, 750)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 || shortened != 0 || !reflect.DeepEqual(got, sameStart[1:]) {
		t.Fatalf("same-start resolution = %#v, %d, %d", got, dropped, shortened)
	}
}

func TestSortAndValidateCues(t *testing.T) {
	cues := []cue{
		{startMS: 1000, endMS: 2000, textLines: []string{"z"}},
		{startMS: 0, endMS: 1000, textLines: []string{"b"}},
		{startMS: 0, endMS: 1000, textLines: []string{"a"}},
	}
	sortCues(cues)
	if got := strings.Join([]string{cues[0].textLines[0], cues[1].textLines[0], cues[2].textLines[0]}, ""); got != "abz" {
		t.Fatalf("sortCues() order = %q", got)
	}
	valid := []cue{
		{startMS: 0, endMS: 1000, textLines: []string{"a"}},
		{startMS: 1000, endMS: 2000, textLines: []string{"b"}},
	}
	if err := validateCues(valid, 2000); err != nil {
		t.Fatalf("valid cues rejected: %v", err)
	}
	if err := validateCues([]cue{{startMS: 0, endMS: 2000}, {startMS: 1500, endMS: 2500}}, 2500); err == nil {
		t.Fatal("overlapping cues accepted")
	}
	if err := validateCues(nil, 1000); err == nil {
		t.Fatal("empty cues accepted")
	}
	if err := validateCues([]cue{{startMS: 0, endMS: 2001}}, 1000); err == nil {
		t.Fatal("cue ending over one second after media accepted")
	}
}

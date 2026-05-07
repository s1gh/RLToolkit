package wire

import "testing"

func TestExtractEventName_PascalAndLowercaseKeys(t *testing.T) {
	RegisterAliases(map[string]string{"goalscored": "GoalScored"})

	cases := map[string]string{
		`{"Event":"GoalScored","Data":{}}`: "GoalScored",
		`{"event":"goalscored","data":{}}`: "GoalScored",
		`{"Data":{},"Event":"GoalScored"}`: "GoalScored", // field-order independent
		`{"Event":"_MatchState","Data":{}}`: "_MatchState",
		`{"foo":"bar"}`: "",
	}
	for in, want := range cases {
		if got := ExtractEventName([]byte(in)); got != want {
			t.Errorf("ExtractEventName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanBReplay_AllMarkers(t *testing.T) {
	for _, in := range []string{
		`{"Data":"\"bReplay\":true"}`,
		`{"Data":"\"breplay\":true"}`,
		`{"Event":"X","payload":{"bReplay":true}}`,
	} {
		if !ScanBReplay([]byte(in)) {
			t.Errorf("ScanBReplay(%q) = false, want true", in)
		}
	}
	if ScanBReplay([]byte(`{"bReplay":false}`)) {
		t.Error("ScanBReplay returned true for false flag")
	}
}

func TestExtractMatchGUID(t *testing.T) {
	in := `{"Event":"X","Data":"\"MatchGuid\":\"abcd-1234\""}`
	if got := ExtractMatchGUID([]byte(in)); got != "abcd-1234" {
		t.Errorf("ExtractMatchGUID = %q, want %q", got, "abcd-1234")
	}
}

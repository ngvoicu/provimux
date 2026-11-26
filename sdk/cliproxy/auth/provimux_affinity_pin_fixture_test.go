package auth

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestProvimuxAffinityPinFixture is run by test/provimux's cross-build test, once on this build and
// once on a build with one upstream edit (go test -overlay): PROVIMUX_TEST_PIN_WRITE writes this
// build's pin; PROVIMUX_TEST_PIN_READ checks a pin written by the other build and writes to
// PROVIMUX_TEST_PIN_RESULT "same", "kinds: <the kinds of conversation left out>" or "whole: <why the
// snapshot is ignored>".
func TestProvimuxAffinityPinFixture(t *testing.T) {
	write, read, result := os.Getenv("PROVIMUX_TEST_PIN_WRITE"), os.Getenv("PROVIMUX_TEST_PIN_READ"), os.Getenv("PROVIMUX_TEST_PIN_RESULT")
	if write == "" && read == "" {
		t.Skip("run by test/provimux TestProvimuxAffinityPinAcrossBuilds")
	}
	if write != "" {
		pin, err := ProvimuxAffinityCurrentPin()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(pin)
		if errWrite := os.WriteFile(write, data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		return
	}
	data, err := os.ReadFile(read)
	if err != nil {
		t.Fatal(err)
	}
	var pin ProvimuxAffinityPin
	if errDecode := json.Unmarshal(data, &pin); errDecode != nil {
		t.Fatal(errDecode)
	}
	outcome := "same"
	differs, errCheck := ProvimuxAffinityCheckPin(pin)
	if errCheck != nil {
		outcome = "whole: " + errCheck.Error()
	} else if len(differs) > 0 {
		var kinds []string
		for kind := range differs {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		outcome = "kinds: " + strings.Join(kinds, ",")
	}
	if errWrite := os.WriteFile(result, []byte(outcome), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
}

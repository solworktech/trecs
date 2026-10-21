package main

import "testing"

func TestANameThatIsAPathIsRefused(t *testing.T) {
	for _, bad := range []string{"../x", "a/b", "/abs", `a\b`, "..", "."} {
		if checkSessionName(bad) == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, ok := range []string{"", "2026_10_20_10_00_00", "my recording", "a.b", "..x"} {
		if err := checkSessionName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

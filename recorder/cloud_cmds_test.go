package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"trecs/cloud"
)

func TestARefusedLoginPointsAtForgotPasswordForAccountsMadeWithGitHub(t *testing.T) {
	refused := &cloud.APIError{Status: 401, Code: "invalid_credentials", Message: "Wrong email or password."}
	hint := loginHint(fmt.Errorf("wrapped: %w", refused), "https://trecs.example.com/")
	for _, want := range []string{"GitHub", "https://trecs.example.com/forgot-password"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q lacks %q", hint, want)
		}
	}
	if strings.Contains(hint, "com//forgot") {
		t.Errorf("the server's trailing slash must not be doubled: %q", hint)
	}
	for name, err := range map[string]error{
		"a server error": &cloud.APIError{Status: 500, Message: "boom"},
		"a rate limit":   &cloud.APIError{Status: 429, Message: "slow down"},
		"no connection":  errors.New("connection refused"),
	} {
		if h := loginHint(err, "https://trecs.example.com"); h != "" {
			t.Errorf("%s must not get the hint: %q", name, h)
		}
	}
}

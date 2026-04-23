package gatekeeper

import "testing"

func TestParseBearer_Valid(t *testing.T) {
	scheme, token, ok := parseBearer("Bearer my-token")
	if !ok {
		t.Fatal("expected ok=true for valid Bearer header")
	}
	if scheme != "Bearer" {
		t.Errorf("scheme = %q, want %q", scheme, "Bearer")
	}
	if token != "my-token" {
		t.Errorf("token = %q, want %q", token, "my-token")
	}
}

func TestParseBearer_CaseInsensitive(t *testing.T) {
	_, token, ok := parseBearer("bearer my-token")
	if !ok {
		t.Fatal("expected ok=true for lowercase 'bearer'")
	}
	if token != "my-token" {
		t.Errorf("token = %q, want %q", token, "my-token")
	}
}

func TestParseBearer_Invalid(t *testing.T) {
	cases := []string{
		"",
		"Basic dXNlcjpwYXNz",
		"BearerNoSpace",
		"Bearer",
		"  Bearer token",
	}
	for _, input := range cases {
		_, _, ok := parseBearer(input)
		if ok {
			t.Errorf("parseBearer(%q) = ok, expected failure", input)
		}
	}
}

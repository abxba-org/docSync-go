package baselines

import "testing"

func TestShortSha_ShortValueReturnsUnchanged(t *testing.T) {
	if got := shortSha("abc"); got != "abc" {
		t.Fatalf("shortSha(%q) = %q, want %q", "abc", got, "abc")
	}
}

func TestShortSha_TwelveOrFewerUnchanged(t *testing.T) {
	if got := shortSha("abcdefghijkl"); got != "abcdefghijkl" {
		t.Fatalf("shortSha(12) = %q, want unchanged", got)
	}
}

func TestShortSha_LongValueTruncatedToTwelve(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	if got := shortSha(sha); got != sha[:12] {
		t.Fatalf("shortSha(40) = %q, want %q", got, sha[:12])
	}
}

func TestShortSha_EmptyUnchanged(t *testing.T) {
	if got := shortSha(""); got != "" {
		t.Fatalf("shortSha(\"\") = %q, want empty", got)
	}
}

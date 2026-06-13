package slug

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"my-app", true},
		{"web", true},
		{"a", true},
		{"app1", true},
		{"9app", true}, // a leading digit is fine (DNS-1123 allows it).
		{"shop-web-prod", true},
		{strings.Repeat("a", MaxLen), true},
		{"", false},                            // empty.
		{"My-App", false},                      // uppercase.
		{"my app", false},                      // space.
		{"my_app", false},                      // underscore.
		{"-my-app", false},                     // leading hyphen.
		{"my-app-", false},                     // trailing hyphen.
		{"my.app", false},                      // dot.
		{"café", false},                        // non-ASCII.
		{strings.Repeat("a", MaxLen+1), false}, // too long.
	}
	for _, c := range cases {
		if got := Valid(c.in); got != c.want {
			t.Errorf("Valid(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

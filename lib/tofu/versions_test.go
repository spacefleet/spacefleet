package tofu

import (
	"strings"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	t.Run("empty resolves to the default line", func(t *testing.T) {
		v, ok := ResolveVersion("")
		if !ok {
			t.Fatal("expected the empty version to resolve")
		}
		if v.Minor != DefaultVersion {
			t.Fatalf("empty resolved to %q, want %q", v.Minor, DefaultVersion)
		}
		if v.NativeS3Lock {
			t.Fatal("the 1.9 default line must not report native s3 locking")
		}
	})

	t.Run("every supported line resolves to itself", func(t *testing.T) {
		for _, want := range Versions {
			got, ok := ResolveVersion(want.Minor)
			if !ok {
				t.Fatalf("ResolveVersion(%q) not ok", want.Minor)
			}
			if got != want {
				t.Fatalf("ResolveVersion(%q) = %+v, want %+v", want.Minor, got, want)
			}
		}
	})

	t.Run("unknown version reports not ok", func(t *testing.T) {
		if _, ok := ResolveVersion("1.4"); ok {
			t.Fatal("expected an unsupported version to report ok=false")
		}
	})

	t.Run("registry is well-formed", func(t *testing.T) {
		if _, ok := ResolveVersion(DefaultVersion); !ok {
			t.Fatalf("DefaultVersion %q is not in the registry", DefaultVersion)
		}
		seen := map[string]bool{}
		for _, v := range Versions {
			if seen[v.Minor] {
				t.Fatalf("duplicate registry entry for %q", v.Minor)
			}
			seen[v.Minor] = true
			// Each line pins an exact patch tag of that line, so runs are
			// deterministic and the image actually matches the selected minor.
			if !strings.HasPrefix(v.Image, "ghcr.io/opentofu/opentofu:"+v.Minor+".") {
				t.Fatalf("line %q pins image %q — want an exact %s.x patch tag", v.Minor, v.Image, v.Minor)
			}
		}
	})
}

package replaywatch

import (
	"path/filepath"
	"testing"
)

// fakeFS tracks which paths "exist" for ResolveDir tests.
type fakeFS map[string]bool

func (f fakeFS) exists(p string) bool { return f[filepath.Clean(p)] }

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveDir_ConfiguredOverride(t *testing.T) {
	fs := fakeFS{}
	got, src := ResolveDir("/custom/path", "linux", envFunc(nil), "/home/u", fs.exists)
	if src != "configured" {
		t.Errorf("source = %q, want %q", src, "configured")
	}
	if got != "/custom/path" {
		t.Errorf("path = %q, want %q", got, "/custom/path")
	}
}

func TestResolveDir_ConfiguredWinsOverAuto(t *testing.T) {
	autoPath := "/home/u/.local/share/Steam/steamapps/compatdata/252950/pfx/drive_c/users/steamuser/Documents/My Games/Rocket League/TAGame/Demos"
	fs := fakeFS{autoPath: true}
	got, src := ResolveDir("/custom/path", "linux", envFunc(nil), "/home/u", fs.exists)
	if src != "configured" || got != "/custom/path" {
		t.Errorf("got (%q,%q), want (\"/custom/path\",\"configured\")", got, src)
	}
}

func TestResolveDir_LinuxLocalShare(t *testing.T) {
	autoPath := "/home/u/.local/share/Steam/steamapps/compatdata/252950/pfx/drive_c/users/steamuser/Documents/My Games/Rocket League/TAGame/Demos"
	fs := fakeFS{autoPath: true}
	got, src := ResolveDir("", "linux", envFunc(nil), "/home/u", fs.exists)
	if src != "auto" || got != autoPath {
		t.Errorf("got (%q,%q), want (%q,\"auto\")", got, src, autoPath)
	}
}

func TestResolveDir_LinuxDotSteamFallback(t *testing.T) {
	dotSteam := "/home/u/.steam/steam/steamapps/compatdata/252950/pfx/drive_c/users/steamuser/Documents/My Games/Rocket League/TAGame/Demos"
	fs := fakeFS{dotSteam: true}
	got, src := ResolveDir("", "linux", envFunc(nil), "/home/u", fs.exists)
	if src != "auto" || got != dotSteam {
		t.Errorf("got (%q,%q), want (%q,\"auto\")", got, src, dotSteam)
	}
}

func TestResolveDir_LinuxMyDocumentsFallback(t *testing.T) {
	// Older Wine prefixes use "My Documents" instead of "Documents".
	myDocs := "/home/u/.local/share/Steam/steamapps/compatdata/252950/pfx/drive_c/users/steamuser/My Documents/My Games/Rocket League/TAGame/Demos"
	fs := fakeFS{myDocs: true}
	got, src := ResolveDir("", "linux", envFunc(nil), "/home/u", fs.exists)
	if src != "auto" || got != myDocs {
		t.Errorf("got (%q,%q), want (%q,\"auto\")", got, src, myDocs)
	}
}

func TestResolveDir_LinuxDocumentsPreferredOverMyDocuments(t *testing.T) {
	docs := "/home/u/.local/share/Steam/steamapps/compatdata/252950/pfx/drive_c/users/steamuser/Documents/My Games/Rocket League/TAGame/Demos"
	myDocs := "/home/u/.local/share/Steam/steamapps/compatdata/252950/pfx/drive_c/users/steamuser/My Documents/My Games/Rocket League/TAGame/Demos"
	fs := fakeFS{docs: true, myDocs: true}
	got, src := ResolveDir("", "linux", envFunc(nil), "/home/u", fs.exists)
	if src != "auto" || got != docs {
		t.Errorf("got (%q,%q), want (%q,\"auto\")", got, src, docs)
	}
}

func TestResolveDir_LinuxSteamCompatDataPath(t *testing.T) {
	scdp := "/run/.../compatdata/252950"
	candidate := scdp + "/pfx/drive_c/users/steamuser/Documents/My Games/Rocket League/TAGame/Demos"
	fs := fakeFS{candidate: true}
	got, src := ResolveDir("", "linux", envFunc(map[string]string{"STEAM_COMPAT_DATA_PATH": scdp}), "/home/u", fs.exists)
	if src != "auto" || got != candidate {
		t.Errorf("got (%q,%q), want (%q,\"auto\")", got, src, candidate)
	}
}

func TestResolveDir_LinuxNothingExists(t *testing.T) {
	got, src := ResolveDir("", "linux", envFunc(nil), "/home/u", fakeFS{}.exists)
	if src != "none" || got != "" {
		t.Errorf("got (%q,%q), want (\"\",\"none\")", got, src)
	}
}

func TestResolveDir_WindowsCanonical(t *testing.T) {
	want := `C:\Users\bob\Documents\My Games\Rocket League\TAGame\Demos`
	fs := fakeFS{want: true}
	env := envFunc(map[string]string{"USERPROFILE": `C:\Users\bob`})
	got, src := ResolveDir("", "windows", env, "", fs.exists)
	if src != "auto" || got != want {
		t.Errorf("got (%q,%q), want (%q,\"auto\")", got, src, want)
	}
}

func TestResolveDir_WindowsMissing(t *testing.T) {
	env := envFunc(map[string]string{"USERPROFILE": `C:\Users\bob`})
	got, src := ResolveDir("", "windows", env, "", fakeFS{}.exists)
	if src != "none" || got != "" {
		t.Errorf("got (%q,%q), want (\"\",\"none\")", got, src)
	}
}

func TestResolveDir_DarwinNoConfigured(t *testing.T) {
	got, src := ResolveDir("", "darwin", envFunc(nil), "/Users/u", fakeFS{}.exists)
	if src != "none" || got != "" {
		t.Errorf("got (%q,%q), want (\"\",\"none\")", got, src)
	}
}

func TestResolveDir_DarwinHonorsConfigured(t *testing.T) {
	got, src := ResolveDir("/x/y", "darwin", envFunc(nil), "/Users/u", fakeFS{}.exists)
	if src != "configured" || got != "/x/y" {
		t.Errorf("got (%q,%q), want (\"/x/y\",\"configured\")", got, src)
	}
}

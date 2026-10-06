package media

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPreferredSourcePrefersFLAC(t *testing.T) {
	root := t.TempDir()
	flac := filepath.Join(root, "artist", "album", "song.flac")
	m4a := filepath.Join(root, "artist", "album", "song.m4a")
	touch(t, flac)
	touch(t, m4a)

	if got := PreferredSource(root, m4a); got != flac {
		t.Fatalf("PreferredSource(m4a) = %q, want %q", got, flac)
	}
	if got := PreferredSource(root, flac); got != flac {
		t.Fatalf("PreferredSource(flac) = %q, want %q", got, flac)
	}
}

func TestPreferredSourceWithoutTwin(t *testing.T) {
	root := t.TempDir()
	m4a := filepath.Join(root, "song.m4a")
	touch(t, m4a)

	if got := PreferredSource(root, m4a); got != m4a {
		t.Fatalf("PreferredSource(m4a) = %q, want itself", got)
	}
}

func TestSourcePathFindsNonFLACTwin(t *testing.T) {
	lossless := t.TempDir()
	lossy := t.TempDir()
	m4a := filepath.Join(lossless, "artist", "album", "song.m4a")
	opus := filepath.Join(lossy, "artist", "album", "song.opus")
	touch(t, m4a)
	touch(t, opus)

	got, ok := SourcePath(lossless, lossy, opus)
	if !ok || got != m4a {
		t.Fatalf("SourcePath = %q, %v; want %q, true", got, ok, m4a)
	}
}

func TestSourcePathMissing(t *testing.T) {
	lossless := t.TempDir()
	lossy := t.TempDir()
	opus := filepath.Join(lossy, "song.opus")
	touch(t, opus)

	if got, ok := SourcePath(lossless, lossy, opus); ok {
		t.Fatalf("SourcePath = %q, true; want no source", got)
	}
}

func TestOpusPathSwapsAnyLosslessExtension(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	src := filepath.Join(srcRoot, "artist", "album", "song.m4a")

	got, err := OpusPath(srcRoot, dstRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dstRoot, "artist", "album", "song.opus")
	if got != want {
		t.Fatalf("OpusPath = %q, want %q", got, want)
	}
}

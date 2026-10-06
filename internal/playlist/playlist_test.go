package playlist

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRewriteMatchesForeignLossyExtension(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	track := filepath.Join(srcRoot, "chryst", "damage", "damage_(ft._sophie_meiers).opus")
	if err := os.MkdirAll(filepath.Dir(track), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(track, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	m := Mapping{
		SrcAudioRoot: srcRoot,
		DstAudioRoot: dstRoot,
		SrcExts:      []string{".opus"},
		DstExts:      []string{".flac"},
	}
	got := m.rewrite("primary/Music/_lossy/chryst/damage/damage_(ft._sophie_meiers).mp3", srcRoot, dstRoot)
	want := filepath.ToSlash(filepath.Join("..", "_lossy", "chryst", "damage", "damage_(ft._sophie_meiers).mp3"))
	if got != want {
		t.Fatalf("rewrite() = %q, want %q", got, want)
	}
}

func TestRewriteForeignLossyPathWithoutMirrorTwin(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	m := Mapping{
		SrcAudioRoot: srcRoot,
		DstAudioRoot: dstRoot,
		SrcExts:      []string{".opus"},
		DstExts:      []string{".flac"},
	}

	got := m.rewrite("primary/Music/_lossy/girls_und_panzer_original_soundtrack/panzerlied.mp3", srcRoot, dstRoot)
	want := filepath.ToSlash(filepath.Join("..", "_lossy", "girls_und_panzer_original_soundtrack", "panzerlied.mp3"))
	if got != want {
		t.Fatalf("rewrite() = %q, want %q", got, want)
	}
}

func TestRewriteForeignAudioDirectoryPreservesDirectoryAndExtension(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	m := Mapping{
		SrcAudioRoot: filepath.Join("primary", "Music", "_lossless"),
		DstAudioRoot: filepath.Join("primary", "Music", "_lossy_opus"),
		SrcExts:      []string{".flac"},
		DstExts:      []string{".opus"},
	}

	got := m.rewrite("primary/Music/_old_mp3s/album/song.mp3", srcRoot, dstRoot)
	want := filepath.ToSlash(filepath.Join("..", "_old_mp3s", "album", "song.mp3"))
	if got != want {
		t.Fatalf("rewrite() = %q, want %q", got, want)
	}
}

func TestRewriteForeignNamedDirectoryPreservesDirectoryAndExtension(t *testing.T) {
	m := Mapping{
		SrcAudioRoot: filepath.Join("primary", "Music", "_lossless"),
		DstAudioRoot: filepath.Join("primary", "Music", "_lossy_opus"),
		SrcExts:      []string{".flac"},
		DstExts:      []string{".opus"},
	}

	got := m.rewrite("primary/Music/folder2/album/song.mp3", "playlists", "playlists")
	want := filepath.ToSlash(filepath.Join("..", "folder2", "album", "song.mp3"))
	if got != want {
		t.Fatalf("rewrite() = %q, want %q", got, want)
	}
}

func TestRewriteM4AToOpus(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	track := filepath.Join(srcRoot, "artist", "album", "song.m4a")
	if err := os.MkdirAll(filepath.Dir(track), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(track, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	m := Mapping{
		SrcAudioRoot: srcRoot,
		DstAudioRoot: dstRoot,
		SrcExts:      []string{".flac", ".m4a"},
		DstExts:      []string{".opus"},
	}

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	got := m.rewrite("artist/album/song.m4a", srcDir, dstDir)
	want, err := filepath.Rel(dstDir, filepath.Join(dstRoot, "artist", "album", "song.opus"))
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.ToSlash(want) {
		t.Fatalf("rewrite() = %q, want %q", got, filepath.ToSlash(want))
	}
}

func TestRewriteOpusToExistingM4ATwin(t *testing.T) {
	lossyRoot := t.TempDir()
	losslessRoot := t.TempDir()
	opus := filepath.Join(lossyRoot, "artist", "album", "song.opus")
	m4a := filepath.Join(losslessRoot, "artist", "album", "song.m4a")
	for _, p := range []string{opus, m4a} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := Mapping{
		SrcAudioRoot: lossyRoot,
		DstAudioRoot: losslessRoot,
		SrcExts:      []string{".opus"},
		DstExts:      []string{".flac", ".m4a"},
	}

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	got := m.rewrite("artist/album/song.opus", srcDir, dstDir)
	want, err := filepath.Rel(dstDir, filepath.Join(losslessRoot, "artist", "album", "song.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.ToSlash(want) {
		t.Fatalf("rewrite() = %q, want %q", got, filepath.ToSlash(want))
	}
}

func TestRewriteOpusFallsBackToFLACWithoutTwin(t *testing.T) {
	lossyRoot := t.TempDir()
	losslessRoot := t.TempDir()
	opus := filepath.Join(lossyRoot, "artist", "album", "song.opus")
	if err := os.MkdirAll(filepath.Dir(opus), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opus, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	m := Mapping{
		SrcAudioRoot: lossyRoot,
		DstAudioRoot: losslessRoot,
		SrcExts:      []string{".opus"},
		DstExts:      []string{".flac", ".m4a"},
	}

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	got := m.rewrite("artist/album/song.opus", srcDir, dstDir)
	want, err := filepath.Rel(dstDir, filepath.Join(losslessRoot, "artist", "album", "song.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.ToSlash(want) {
		t.Fatalf("rewrite() = %q, want %q", got, filepath.ToSlash(want))
	}
}

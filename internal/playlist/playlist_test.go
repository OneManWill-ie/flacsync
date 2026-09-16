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
		SrcExt:       ".opus",
		DstExt:       ".flac",
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
		SrcExt:       ".opus",
		DstExt:       ".flac",
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
		SrcExt:       ".flac",
		DstExt:       ".opus",
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
		SrcExt:       ".flac",
		DstExt:       ".opus",
	}

	got := m.rewrite("primary/Music/folder2/album/song.mp3", "playlists", "playlists")
	want := filepath.ToSlash(filepath.Join("..", "folder2", "album", "song.mp3"))
	if got != want {
		t.Fatalf("rewrite() = %q, want %q", got, want)
	}
}

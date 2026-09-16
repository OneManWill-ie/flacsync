// Package playlist translates M3U playlists between the lossless and lossy
// libraries and implements the loop prevention that keeps a bidirectional
// sync from ping-ponging forever.
//
// The rule is simple: every time we write a playlist we remember the SHA256 of
// the bytes we wrote. When the watcher later reports a change to that file we
// hash it again; if the hash matches what we wrote, the event is our own echo
// and is dropped. Anything else is a real edit and gets translated.
package playlist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Exts are the playlist file extensions the engine handles.
var Exts = []string{".m3u", ".m3u8"}

// IsPlaylist reports whether path looks like a playlist file.
func IsPlaylist(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range Exts {
		if ext == e {
			return true
		}
	}
	return false
}

// HashStore records the content hash of every playlist the engine has written
// or consumed. It is safe for concurrent use.
type HashStore struct {
	mu   sync.Mutex
	sums map[string]string
}

// NewHashStore returns an empty store.
func NewHashStore() *HashStore {
	return &HashStore{sums: make(map[string]string)}
}

// Remember stores the hash associated with path.
func (h *HashStore) Remember(path, sum string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sums[filepath.Clean(path)] = sum
}

// Matches reports whether sum is the hash we last saw for path, i.e. whether
// this change is our own echo.
func (h *HashStore) Matches(path, sum string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	known, ok := h.sums[filepath.Clean(path)]
	return ok && known == sum
}

// Forget drops the record for path, e.g. after it is deleted.
func (h *HashStore) Forget(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sums, filepath.Clean(path))
}

// HashBytes returns the hex SHA256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile returns the hex SHA256 of a file's contents.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// Mapping describes how the media entries inside a playlist must be rewritten:
// from one audio root and extension to the other.
type Mapping struct {
	SrcAudioRoot string // e.g. /music/_lossless
	DstAudioRoot string // e.g. /music/_lossy_opus
	SrcExt       string // ".flac"
	DstExt       string // ".opus"
}

// Invert returns the mapping for the opposite direction.
func (m Mapping) Invert() Mapping {
	return Mapping{
		SrcAudioRoot: m.DstAudioRoot,
		DstAudioRoot: m.SrcAudioRoot,
		SrcExt:       m.DstExt,
		DstExt:       m.SrcExt,
	}
}

// Translate reads the playlist at src, rewrites every media entry so it points
// at the mirrored library, and writes the result to dst.
//
// Comments and #EXTINF directives are preserved verbatim. Entries are emitted
// relative to dst's own directory with forward slashes, which is what phone
// players expect. Anything that cannot be mapped (a URL, or a file outside the
// library) is copied through unchanged rather than dropped.
//
// The written bytes and the source bytes are both recorded in store, so
// neither side's watcher will treat the result as a new user edit.
func Translate(src, dst string, m Mapping, store *HashStore) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read playlist %s: %w", src, err)
	}

	srcDir := filepath.Dir(src)
	dstDir := filepath.Dir(dst)

	var out bytes.Buffer
	for _, line := range splitLines(string(raw)) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out.WriteString(trimmed)
			out.WriteByte('\n')
			continue
		}
		out.WriteString(m.rewrite(trimmed, srcDir, dstDir))
		out.WriteByte('\n')
	}
	data := out.Bytes()

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create playlist dir %s: %w", dstDir, err)
	}

	// Stage through .tmp so the other side never sees a half-written playlist
	// (and so our own watcher ignores the intermediate file).
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write playlist %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace playlist %s: %w", dst, err)
	}

	store.Remember(dst, HashBytes(data))
	store.Remember(src, HashBytes(raw))
	return nil
}

// rewrite maps a single playlist entry across libraries. It always returns a
// usable line: an entry that cannot be located inside the source library gets
// its extension swapped and is otherwise left alone, which is strictly better
// than dropping the track.
func (m Mapping) rewrite(entry, srcDir, dstDir string) string {
	if strings.Contains(entry, "://") {
		return entry // stream URL, nothing to map
	}

	local := fromAnySlash(entry)
	if unmanagedLossy, ok := m.foreignAudioPath(local); ok {
		return filepath.ToSlash(unmanagedLossy)
	}

	rel, ok := m.locate(local, srcDir)
	if !ok {
		return filepath.ToSlash(m.swapExt(local))
	}

	target := filepath.Join(filepath.Clean(m.DstAudioRoot), m.swapExt(rel))
	out, err := filepath.Rel(dstDir, target)
	if err != nil {
		out = target
	}
	return filepath.ToSlash(out)
}

// locate works out where an entry sits inside the source library, trying three
// interpretations in order of confidence:
//
//  1. a path relative to the playlist file (or an absolute path), the form
//     desktop players write;
//  2. a path relative to the music root, which several mobile players write;
//  3. a trailing fragment of a foreign absolute path, e.g. an Android entry
//     like /storage/emulated/0/Music/Artist/Album/01.opus, matched by walking
//     the components until one resolves inside the library.
//
// Cases 2 and 3 only count when the file really is there, so a guess never
// silently invents a path.
func (m Mapping) locate(local, srcDir string) (string, bool) {
	root := filepath.Clean(m.SrcAudioRoot)

	abs := local
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(srcDir, local)
	}
	if rel, err := relUnder(root, filepath.Clean(abs)); err == nil {
		if exists(filepath.Join(root, rel)) {
			return rel, true
		}
		mapped := replaceExt(rel, m.SrcExt)
		if mapped != rel && exists(filepath.Join(root, mapped)) {
			return mapped, true
		}
	}

	if !filepath.IsAbs(local) && exists(filepath.Join(root, local)) {
		return filepath.Clean(local), true
	}

	parts := strings.Split(filepath.ToSlash(filepath.Clean(local)), "/")
	for i := 1; i < len(parts); i++ {
		suffix := filepath.Join(parts[i:]...)
		if suffix == "" {
			continue
		}
		if exists(filepath.Join(root, suffix)) {
			return suffix, true
		}
		// Players may export a path from a different lossy library, such as
		// _lossy/...track.mp3. Match its album-relative stem in our mirror,
		// where the same track has the mapping's source extension.
		mapped := replaceExt(suffix, m.SrcExt)
		if mapped != suffix && exists(filepath.Join(root, mapped)) {
			return mapped, true
		}
	}

	return "", false
}

func (m Mapping) foreignAudioPath(local string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(local)), "/")
	for i, part := range parts {
		if !strings.EqualFold(part, "music") || i+1 >= len(parts) {
			continue
		}
		foreign := parts[i+1:]
		if strings.EqualFold(foreign[0], filepath.Base(m.SrcAudioRoot)) ||
			strings.EqualFold(foreign[0], filepath.Base(m.DstAudioRoot)) {
			return "", false
		}
		return filepath.Join("..", filepath.Join(foreign...)), true
	}
	return "", false
}

// fromAnySlash converts an entry to the host's path syntax. A backslash is a
// legal character in a Unix filename, so it is only treated as a separator
// when the entry looks like a Windows path: a drive letter, a UNC prefix, or
// backslashes with no forward slash anywhere. Desktop players on Windows write
// these, and the engine may well be running on Linux.
func fromAnySlash(entry string) string {
	if looksLikeWindowsPath(entry) {
		entry = strings.ReplaceAll(entry, `\`, "/")
	}
	return filepath.FromSlash(entry)
}

func looksLikeWindowsPath(entry string) bool {
	if !strings.Contains(entry, `\`) {
		return false
	}
	if strings.HasPrefix(entry, `\\`) { // UNC share
		return true
	}
	if len(entry) > 2 && entry[1] == ':' && isLetter(entry[0]) {
		return true
	}
	return !strings.Contains(entry, "/")
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func (m Mapping) swapExt(p string) string {
	if !strings.EqualFold(filepath.Ext(p), m.SrcExt) {
		return p
	}
	return strings.TrimSuffix(p, filepath.Ext(p)) + m.DstExt
}

func replaceExt(p, ext string) string {
	old := filepath.Ext(p)
	if old == "" || ext == "" {
		return p
	}
	return strings.TrimSuffix(p, old) + ext
}

func relUnder(root, p string) (string, error) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", p, root)
	}
	return rel, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// splitLines normalises CRLF and drops a single trailing blank line.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimPrefix(s, "\ufeff") // strip UTF-8 BOM
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

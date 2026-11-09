package tocli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Kind is what an attachment holds.
type Kind int

const (
	KindPaste Kind = iota
	KindFile
	KindImage
)

// String names the kind for messages shown to the user.
func (k Kind) String() string {
	switch k {
	case KindPaste:
		return "teks tempelan"
	case KindImage:
		return "gambar"
	}
	return "file"
}

// Defaults for turning a paste into an attachment.
const (
	DefaultPasteLines = 20
	DefaultPasteBytes = 2048
)

const (
	maxFileBytes   = 100 * 1024 * 1024
	previewBytes   = 8 * 1024
	unsafeNameChar = "-"
)

var (
	imageExtensions = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}
	unsafeName      = regexp.MustCompile(`[^\p{L}\p{N}._-]+`)
)

// Attachment is one item added to a draft. Data holds pasted text or a
// clipboard image; a file stays at SourcePath until the draft is sent.
type Attachment struct {
	Kind       Kind
	ID         int
	Marker     string
	Name       string
	Data       []byte
	SourcePath string
	Lines      int
	Size       int64
	SavedPath  string
}

// IsLongPaste reports whether text should become an attachment.
func IsLongPaste(text string, maxLines, maxBytes int) bool {
	return CountLines(text) >= maxLines || len(text) >= maxBytes
}

// CountLines counts the lines of text.
func CountLines(text string) int {
	return strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
}

// NormalizePaste converts terminal line endings to \n. Consoles deliver paste
// with \r or \r\n and can leave NUL bytes behind.
func NormalizePaste(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// NewPaste wraps pasted text as an attachment.
func NewPaste(id int, text string) Attachment {
	lines := CountLines(text)
	return Attachment{
		Kind: KindPaste, ID: id, Lines: lines, Size: int64(len(text)), Data: []byte(text),
		Name:   fmt.Sprintf("paste-%d.txt", id),
		Marker: fmt.Sprintf("[Paste #%d · %d baris]", id, lines),
	}
}

// NewImage wraps a PNG read from the clipboard.
func NewImage(id int, png []byte) Attachment {
	return Attachment{
		Kind: KindImage, ID: id, Size: int64(len(png)), Data: png,
		Name:   fmt.Sprintf("image-%d.png", id),
		Marker: fmt.Sprintf("[Image #%d · %d KB]", id, (len(png)+1023)/1024),
	}
}

// NewFile checks a user-given path. Quotes are removed because terminals add
// them when a path is dragged in.
func NewFile(id int, path string) (Attachment, error) {
	path = strings.Trim(strings.TrimSpace(path), `"'`)
	if path == "" {
		return Attachment{}, errors.New("path wajib diisi")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return Attachment{}, err
		}
		path = filepath.Join(home, path[1:])
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Attachment{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Attachment{}, fmt.Errorf("file tidak bisa dibaca: %s", absolute)
	}
	if info.IsDir() {
		return Attachment{}, fmt.Errorf("%s adalah folder, bukan file", absolute)
	}
	if info.Size() > maxFileBytes {
		return Attachment{}, fmt.Errorf("file lebih besar dari %d MB", maxFileBytes/1024/1024)
	}

	base := filepath.Base(absolute)
	kind, label := KindFile, "File"
	if imageExtensions[strings.ToLower(filepath.Ext(base))] {
		kind, label = KindImage, "Image"
	}
	display := strings.NewReplacer("[", "(", "]", ")").Replace(base)
	return Attachment{
		Kind: kind, ID: id, SourcePath: absolute, Size: info.Size(),
		Name:   fmt.Sprintf("file-%d-%s", id, unsafeName.ReplaceAllString(base, unsafeNameChar)),
		Marker: fmt.Sprintf("[%s #%d · %s]", label, id, display),
	}, nil
}

// Preview returns readable text for showing an attachment.
func Preview(a Attachment) string {
	data := a.Data
	if a.SourcePath != "" {
		file, err := os.Open(a.SourcePath)
		if err != nil {
			return "File tidak bisa dibaca: " + err.Error()
		}
		defer file.Close()
		buffer := make([]byte, previewBytes)
		n, _ := io.ReadFull(file, buffer)
		data = buffer[:n]
	}
	summary := fmt.Sprintf("%s · %d byte", a.Kind, a.Size)
	if a.Kind == KindImage || !utf8.Valid(trimPartialRune(data)) {
		return summary + "\n\nIsi tidak bisa ditampilkan sebagai teks."
	}
	if len(data) > previewBytes {
		data = data[:previewBytes]
	}
	return summary + "\n\n" + strings.ToValidUTF8(string(data), "")
}

// trimPartialRune drops a character cut in half at the end of a sample, so a
// valid text file is not mistaken for binary.
func trimPartialRune(data []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(data) > 0; i++ {
		if r, size := utf8.DecodeLastRune(data); r != utf8.RuneError || size != 1 {
			break
		}
		data = data[:len(data)-1]
	}
	return data
}

// NodeDir returns where a node's attachments live. The ID is validated so a
// bad value cannot point outside the attachments folder.
func NodeDir(baseDir, nodeID string) (string, error) {
	id, err := uuid.Parse(nodeID)
	if err != nil {
		return "", fmt.Errorf("node id tidak valid: %q", nodeID)
	}
	return filepath.Join(baseDir, id.String()), nil
}

// Save writes a draft's attachments under the node's folder and returns them
// with SavedPath filled in. On failure nothing is left behind.
func Save(baseDir, nodeID string, attachments []Attachment) ([]Attachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	dir, err := NodeDir(baseDir, nodeID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("buat folder attachment: %w", err)
	}
	saved := make([]Attachment, len(attachments))
	for i, attachment := range attachments {
		attachment.SavedPath = filepath.Join(dir, attachment.Name)
		if err := writeAttachment(attachment); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("simpan %s: %w", attachment.Marker, err)
		}
		saved[i] = attachment
	}
	return saved, nil
}

func writeAttachment(a Attachment) error {
	if a.SourcePath == "" {
		return os.WriteFile(a.SavedPath, a.Data, 0o600)
	}
	source, err := os.Open(a.SourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(a.SavedPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

// HasSaved reports whether a node has stored attachments.
func HasSaved(baseDir, nodeID string) bool {
	dir, err := NodeDir(baseDir, nodeID)
	if err != nil {
		return false
	}
	_, err = os.Stat(dir)
	return err == nil
}

// RemoveSaved deletes all of a node's stored attachments.
func RemoveSaved(baseDir, nodeID string) error {
	dir, err := NodeDir(baseDir, nodeID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

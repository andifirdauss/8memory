package tocli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ErrNoImage means the clipboard holds no image.
var ErrNoImage = errors.New("clipboard tidak berisi gambar")

const (
	clipboardTimeout = 10 * time.Second
	maxImageBytes    = 20 * 1024 * 1024
	noImageExitCode  = 3

	windowsClipboardScript = `Add-Type -AssemblyName System.Windows.Forms,System.Drawing
$image = [System.Windows.Forms.Clipboard]::GetImage()
if ($null -eq $image) { exit 3 }
$buffer = New-Object System.IO.MemoryStream
$image.Save($buffer, [System.Drawing.Imaging.ImageFormat]::Png)
$output = [Console]::OpenStandardOutput()
$output.Write($buffer.ToArray(), 0, [int]$buffer.Length)
$output.Flush()`
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// ReadClipboardImage returns the clipboard's image as PNG. Terminals only send
// text on paste, so each OS's clipboard has to be asked directly.
func ReadClipboardImage(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()

	var (
		data []byte
		err  error
	)
	switch runtime.GOOS {
	case "windows":
		// The Windows clipboard API requires a single-threaded apartment.
		data, err = runClipboard(ctx, "powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", windowsClipboardScript)
	case "darwin":
		data, err = readMacClipboard(ctx)
	default:
		data, err = readLinuxClipboard(ctx)
	}
	if err != nil {
		return nil, err
	}
	return validatePNG(data)
}

func validatePNG(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrNoImage
	}
	if !bytes.HasPrefix(data, pngSignature) {
		return nil, errors.New("isi clipboard bukan gambar PNG")
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("gambar lebih besar dari %d MB", maxImageBytes/1024/1024)
	}
	return data, nil
}

func runClipboard(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == noImageExitCode:
		return nil, ErrNoImage
	case errors.Is(err, exec.ErrNotFound):
		return nil, fmt.Errorf("perintah %s tidak ditemukan", name)
	case err != nil:
		return nil, fmt.Errorf("baca clipboard dengan %s: %w", name, err)
	}
	return out, nil
}

func readLinuxClipboard(ctx context.Context) ([]byte, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err != nil {
			return nil, errors.New("butuh wl-paste (paket wl-clipboard) untuk membaca gambar")
		}
		out, err := runClipboard(ctx, "wl-paste", "--no-newline", "--type", "image/png")
		return emptyWhenMissing(out, err)
	}
	if _, err := exec.LookPath("xclip"); err != nil {
		return nil, errors.New("butuh xclip untuk membaca gambar")
	}
	out, err := runClipboard(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	return emptyWhenMissing(out, err)
}

// emptyWhenMissing treats a failed read as an empty clipboard, because these
// tools exit non-zero when the requested image type is not available.
func emptyWhenMissing(out []byte, err error) ([]byte, error) {
	var exit *exec.ExitError
	if err != nil && errors.As(errors.Unwrap(err), &exit) {
		return nil, ErrNoImage
	}
	return out, err
}

func readMacClipboard(ctx context.Context) ([]byte, error) {
	dir, err := os.MkdirTemp("", "8memory-clipboard-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "clipboard.png")
	script := fmt.Sprintf(`set target to open for access POSIX file %q with write permission
write (the clipboard as «class PNGf») to target
close access target`, path)
	if _, err := runClipboard(ctx, "osascript", "-e", script); err != nil {
		return nil, ErrNoImage
	}
	return os.ReadFile(path)
}

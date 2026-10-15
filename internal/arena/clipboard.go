package arena

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type clipboardResult struct {
	Text       string
	Attachment *Attachment
	Err        error
}

// Read AppKit's pasteboard directly; pbpaste alone discards copied images.
// TIFF (common for macOS screenshots) is converted to PNG by the OS.
const macClipboardScript = `ObjC.import('AppKit');
function run() {
 var p = $.NSPasteboard.generalPasteboard;
 var types = [['public.png','image/png'],['public.jpeg','image/jpeg'],['com.compuserve.gif','image/gif'],['org.webmproject.webp','image/webp']];
 for (var i=0; i<types.length; i++) {
  var d = p.dataForType(types[i][0]);
  if (!d.isNil()) {
   if (Number(d.length) > 10485760) throw Error('Clipboard image exceeds 10 MiB');
   return JSON.stringify({mime:types[i][1], data:ObjC.unwrap(d.base64EncodedStringWithOptions(0))});
  }
 }
 var tiff = p.dataForType('public.tiff');
 if (!tiff.isNil()) {
  if (Number(tiff.length) > 33554432) throw Error('Clipboard TIFF exceeds 32 MiB');
  var rep = $.NSBitmapImageRep.imageRepWithData(tiff);
  if (rep.isNil()) throw Error('Could not decode clipboard image');
  var png = rep.representationUsingTypeProperties($.NSPNGFileType, $.NSDictionary.dictionary);
  if (png.isNil() || Number(png.length) > 10485760) throw Error('Clipboard image exceeds 10 MiB');
  return JSON.stringify({mime:'image/png', data:ObjC.unwrap(png.base64EncodedStringWithOptions(0))});
 }
 var text = p.stringForType('public.utf8-plain-text');
 return JSON.stringify({text:text.isNil() ? '' : ObjC.unwrap(text)});
}`

type clipboardCommand func(context.Context, string, ...string) ([]byte, error)

func runClipboardCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(stdout, 15<<20))
	if len(data) >= 15<<20 || err != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return nil, err
	}
	if len(data) >= 15<<20 {
		return nil, fmt.Errorf("clipboard content exceeds size limit")
	}
	if waitErr != nil {
		return nil, fmt.Errorf("clipboard reader failed (%s); copy an image or text and try again", name)
	}
	return data, nil
}

func readClipboard(ctx context.Context, platform string, run clipboardCommand) clipboardResult {
	var mime string
	var data []byte
	if platform == "darwin" {
		b, err := run(ctx, "osascript", "-l", "JavaScript", "-e", macClipboardScript)
		if err != nil {
			return clipboardResult{Err: err}
		}
		var payload struct {
			MIME string `json:"mime"`
			Data string `json:"data"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			return clipboardResult{Err: fmt.Errorf("invalid clipboard response")}
		}
		if payload.MIME == "" {
			return clipboardResult{Text: payload.Text}
		}
		mime = payload.MIME
		data, err = base64.StdEncoding.DecodeString(payload.Data)
		if err != nil {
			return clipboardResult{Err: fmt.Errorf("invalid clipboard image")}
		}
	} else if platform == "linux" {
		// Probe Wayland first, then X11. Never run shell expansions.
		for _, reader := range []string{"wl-paste", "xclip"} {
			args := []string{"--list-types"}
			if reader == "xclip" {
				args = []string{"-selection", "clipboard", "-o", "-t", "TARGETS"}
			}
			types, err := run(ctx, reader, args...)
			if err != nil {
				continue
			}
			selected := ""
			available := strings.Fields(string(types))
			for _, typ := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "text/plain;charset=utf-8", "UTF8_STRING", "text/plain"} {
				if Has(available, typ) {
					selected = typ
					break
				}
			}
			if selected == "" {
				return clipboardResult{Err: fmt.Errorf("clipboard has no supported image or text")}
			}
			args = []string{"--no-newline", "--type", selected}
			if reader == "xclip" {
				args = []string{"-selection", "clipboard", "-o", "-t", selected}
			}
			data, err = run(ctx, reader, args...)
			if err != nil {
				return clipboardResult{Err: err}
			}
			if !strings.HasPrefix(selected, "image/") {
				return clipboardResult{Text: string(data)}
			}
			mime = selected
			break
		}
		if mime == "" {
			return clipboardResult{Err: fmt.Errorf("clipboard unavailable; install wl-clipboard (Wayland) or xclip (X11), or use /attach PATH")}
		}
	} else {
		return clipboardResult{Err: fmt.Errorf("clipboard image paste is supported on macOS and Linux; use /attach PATH")}
	}
	if len(data) == 0 || len(data) > maxAttachmentBytes {
		return clipboardResult{Err: fmt.Errorf("clipboard image is empty or exceeds 10 MiB")}
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}[mime]
	if ext == "" {
		return clipboardResult{Err: fmt.Errorf("unsupported clipboard image type")}
	}
	a := attachmentFromBytes("clipboard."+ext, data)
	if a.MediaType != mime {
		return clipboardResult{Err: fmt.Errorf("clipboard image does not match its declared type")}
	}
	return clipboardResult{Attachment: &a}
}

func (m LiveModel) pasteClipboard() tea.Cmd {
	if m.ClipboardBusy {
		return nil
	}
	ctx := m.Context
	if ctx == nil {
		ctx = context.Background()
	}
	operation := func() tea.Msg {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return readClipboard(bounded, runtime.GOOS, runClipboardCommand)
	}
	if m.Session != nil {
		return m.Session.action(operation)
	}
	return operation
}

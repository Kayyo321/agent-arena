package arena

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestClipboardFormatsAndTextFallback(t *testing.T) {
	ctx := context.Background()
	image := readClipboard(ctx, "darwin", func(context.Context, string, ...string) ([]byte, error) {
		return JSON(map[string]any{"mime": "image/png", "data": tinyPNG}), nil
	})
	if image.Err != nil || image.Attachment == nil || image.Attachment.MediaType != "image/png" {
		t.Fatalf("image: %+v", image)
	}
	m := inputText(LiveModel{}, "keep this draft")
	next, _ := m.Update(image)
	m = next.(LiveModel)
	if m.Prompt != "keep this draft" || len(m.Attachments) != 1 {
		t.Fatal("paste replaced draft")
	}
	text := readClipboard(ctx, "darwin", func(context.Context, string, ...string) ([]byte, error) {
		return JSON(map[string]string{"text": "first\nsecond"}), nil
	})
	next, _ = m.Update(text)
	m = next.(LiveModel)
	if m.Prompt != "keep this draftfirst\nsecond" {
		t.Fatal("text paste")
	}
	bad := readClipboard(ctx, "darwin", func(context.Context, string, ...string) ([]byte, error) {
		return JSON(map[string]string{"mime": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("not image"))}), nil
	})
	if bad.Err == nil {
		t.Fatal("accepted mismatched image")
	}
	calls := []string{}
	linux := readClipboard(ctx, "linux", func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "wl-paste" {
			return nil, fmt.Errorf("unavailable")
		}
		if args[len(args)-1] == "TARGETS" {
			return []byte("image/png\nUTF8_STRING\n"), nil
		}
		return base64.StdEncoding.DecodeString(tinyPNG)
	})
	if linux.Err != nil || linux.Attachment == nil || len(calls) != 3 || !strings.Contains(calls[2], "-t image/png") {
		t.Fatalf("X11 fallback: %+v %v", linux, calls)
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil || !next.(LiveModel).ClipboardBusy || next.(LiveModel).ChangeDetail {
		t.Fatal("Ctrl+V not clipboard")
	}
}

// Uses an isolated named pasteboard, never the user's clipboard. This exercises
// AppKit and the actual osascript bridge on macOS without any model inference.
func TestMacClipboardNativePNGAndTIFF(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native AppKit test")
	}
	for _, format := range []string{"png", "tiff", "text"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			name := "arena-test-" + ID("")
			initialize := fmt.Sprintf("var p = $.NSPasteboard.pasteboardWithName(%q); p.clearContents;", name)
			if format == "text" {
				initialize += `p.setStringForType('first\nsecond', 'public.utf8-plain-text');`
			} else {
				initialize += fmt.Sprintf("var fixture=$.NSData.alloc.initWithBase64EncodedStringOptions(%q,0);", tinyPNG)
				if format == "tiff" {
					initialize += `fixture=$.NSBitmapImageRep.imageRepWithData(fixture).TIFFRepresentation;`
				}
				initialize += fmt.Sprintf("p.setDataForType(fixture, %q);", map[string]string{"png": "public.png", "tiff": "public.tiff"}[format])
			}
			script := strings.Replace(macClipboardScript, "var p = $.NSPasteboard.generalPasteboard;", initialize, 1)
			result := readClipboard(ctx, "darwin", func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return runClipboardCommand(ctx, name, "-l", "JavaScript", "-e", script)
			})
			_, _ = runClipboardCommand(ctx, "osascript", "-l", "JavaScript", "-e", fmt.Sprintf("ObjC.import('AppKit'); $.NSPasteboard.pasteboardWithName(%q).releaseGlobally;", name))
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			if format == "text" {
				if result.Text != "first\nsecond" {
					t.Fatal("native text failed")
				}
			} else if result.Attachment == nil || result.Attachment.MediaType != "image/png" {
				t.Fatal("native image missing")
			}
		})
	}
}

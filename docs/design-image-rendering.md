# Image Rendering with Kitty Graphics Protocol

## Overview

Display images from Claude sessions (screenshots, generated images) in a modal overlay
using the Kitty graphics protocol, with fallback to `open` command for unsupported terminals.

## Current State

- Images stored as base64 in JSONL session files
- `session.ExtractImageToTemp()` decodes and writes to temp file
- `openCachedImage()` in `app.go` opens with macOS `open` command
- Image blocks rendered as `[Image: image/png]` text placeholders

## Architecture

### Terminal Detection (`internal/kitty/detect.go`)

```
Supported() bool
```

Query terminal capabilities via Kitty graphics protocol query mode:
- Send: `\033_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\033\\`
- If terminal responds, Kitty graphics is supported
- Cache the result for the session lifetime
- Known supported: kitty, WezTerm, ghostty

### Graphics Protocol (`internal/kitty/graphics.go`)

```
DisplayImage(path string, x, y, maxW, maxH int) string  // escape sequence
ClearImage(id int) string                                 // clear sequence
```

Use file path mode (`t=f`) to avoid encoding overhead:
- Terminal reads image directly from disk
- No in-memory base64 needed
- Temp file cleaned up on modal close

### Image Modal (`internal/tui/imagemodal.go`)

State fields on App:
```go
imageModalActive bool
imageModalPath   string
imageModalPaths  []string  // for cycling through multiple images
imageModalIndex  int
```

Key handling:
- `Esc` / `q`: close modal, clear image
- `Left` / `Right`: cycle through images in the message
- `o`: open in external viewer (fallback)

### Sizing

Scale to fit 80% of terminal dimensions:
- Read image dimensions with Go's `image.DecodeConfig`
- Compute cell dimensions: terminal reports pixel size via `\033[14t`
- Scale proportionally to fit

### Integration

Replace `openCachedImage()` behavior:
1. Check `kitty.Supported()`
2. If supported: open image modal, emit Kitty escape sequence
3. If not: fall back to `open` command (current behavior)

## Limitations

- One image at a time in modal
- Requires terminal with Kitty graphics support
- Images in alternate screen buffer (Bubble Tea uses alt screen)
  may need special handling
- SSH sessions may not support file path mode (need direct transfer)

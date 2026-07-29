package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

// Screencast records CDP screencast frames (JPEG) and assembles them into an
// animated GIF on stop — the whole pipeline is stdlib (image/jpeg decode,
// Floyd–Steinberg quantization to a 256-color palette, image/gif encode).

const (
	defaultScreencastMaxWidth = 800
	defaultScreencastNthFrame = 2
	defaultScreencastQuality  = 70
	minGIFDelayCS             = 2 // GIF delay unit is 1/100s; browsers clamp below this
)

// handleScreencastFrame stores one frame and acks it. Called from the CDP
// read loop — the ack must go through a goroutine, never inline.
func (m *Manager) handleScreencastFrame(sessionID string, params json.RawMessage) {
	var p struct {
		Data      string `json:"data"`
		SessionID int    `json:"sessionId"`
		Metadata  struct {
			Timestamp float64 `json:"timestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}

	m.col.mu.Lock()
	sc := m.col.screencasts[sessionID]
	if sc != nil && sc.active {
		if len(sc.frames) < maxScreencastFrame {
			if img, err := base64.StdEncoding.DecodeString(p.Data); err == nil {
				sc.frames = append(sc.frames, screencastFrame{data: img, timestamp: p.Metadata.Timestamp})
			}
		} else {
			sc.dropped++
		}
	}
	m.col.mu.Unlock()

	// Ack so Chrome keeps sending frames.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultCallTimeout)
		defer cancel()
		_ = m.client.Call(ctx, sessionID, "Page.screencastFrameAck",
			map[string]any{"sessionId": p.SessionID}, nil)
	}()
}

func (m *Manager) screencastStart(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		FilePath      string `json:"filePath"`
		MaxWidth      int    `json:"maxWidth"`
		EveryNthFrame int    `json:"everyNthFrame"`
		Quality       int    `json:"quality"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.FilePath != "" && !strings.HasSuffix(args.FilePath, ".gif") {
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments,
			"filePath must end in .gif (this implementation records animated GIF, not webm/mp4): %q", args.FilePath)
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	m.col.mu.Lock()
	if sc := m.col.screencasts[p.sessionID]; sc != nil && sc.active {
		m.col.mu.Unlock()
		return nil, toolerr.New(toolerr.CodeInvalidArguments, "a screencast is already recording on this page; call screencast_stop first")
	}
	m.col.screencasts[p.sessionID] = &screencastState{active: true, filePath: args.FilePath}
	m.col.mu.Unlock()

	maxWidth := args.MaxWidth
	if maxWidth <= 0 {
		maxWidth = defaultScreencastMaxWidth
	}
	nth := args.EveryNthFrame
	if nth <= 0 {
		nth = defaultScreencastNthFrame
	}
	quality := args.Quality
	if quality <= 0 {
		quality = defaultScreencastQuality
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	err = m.client.Call(callCtx, p.sessionID, "Page.startScreencast", map[string]any{
		"format":        "jpeg",
		"quality":       quality,
		"maxWidth":      maxWidth,
		"maxHeight":     maxWidth * 2,
		"everyNthFrame": nth,
	}, nil)
	if err != nil {
		m.col.mu.Lock()
		delete(m.col.screencasts, p.sessionID)
		m.col.mu.Unlock()
		return nil, err
	}
	return map[string]any{"recording": true, "maxWidth": maxWidth, "everyNthFrame": nth}, nil
}

func (m *Manager) screencastStop(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct{}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}

	m.col.mu.Lock()
	sc := m.col.screencasts[p.sessionID]
	if sc == nil || !sc.active {
		m.col.mu.Unlock()
		return nil, toolerr.New(toolerr.CodeScreencastNotActive, "no screencast is recording on the selected page")
	}
	sc.active = false
	frames := sc.frames
	dropped := sc.dropped
	filePath := sc.filePath
	delete(m.col.screencasts, p.sessionID)
	m.col.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.client.Call(callCtx, p.sessionID, "Page.stopScreencast", nil, nil); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, toolerr.New(toolerr.CodeScreencastNotActive, "no frames were captured (was the page repainting?)")
	}

	if filePath == "" {
		name := fmt.Sprintf("cast-%s.gif", time.Now().Format("20060102-150405"))
		filePath, err = m.workspaceFile("screencasts", name)
		if err != nil {
			return nil, toolerr.New(toolerr.CodeWorkspaceFailed, err.Error())
		}
	} else if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "create output dir: %v", err)
	}

	g, skipped, durationMS, err := assembleGIF(frames)
	if err != nil {
		return nil, err
	}
	f, err := os.Create(filePath)
	if err != nil {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "create %s: %v", filePath, err)
	}
	defer f.Close()
	if err := gif.EncodeAll(f, g); err != nil {
		return nil, toolerr.Newf(toolerr.CodeWorkspaceFailed, "encode gif: %v", err)
	}

	out := map[string]any{
		"path":       filePath,
		"frames":     len(g.Image),
		"durationMs": durationMS,
	}
	if dropped > 0 {
		out["droppedFrames"] = dropped
	}
	if skipped > 0 {
		out["skippedFrames"] = skipped
	}
	return out, nil
}

// assembleGIF decodes JPEG frames, quantizes them to a 256-color palette
// with Floyd–Steinberg dithering, and derives per-frame delays from the
// capture timestamps. Frames whose size differs from the first frame are
// skipped (viewport changed mid-recording).
func assembleGIF(frames []screencastFrame) (*gif.GIF, int, int, error) {
	g := &gif.GIF{}
	skipped := 0
	var bounds image.Rectangle
	var timestamps []float64

	for _, fr := range frames {
		img, err := jpeg.Decode(bytes.NewReader(fr.data))
		if err != nil {
			skipped++
			continue
		}
		if len(g.Image) == 0 {
			bounds = img.Bounds()
		} else if !img.Bounds().Eq(bounds) {
			skipped++
			continue
		}
		pm := image.NewPaletted(bounds, palette.Plan9)
		draw.FloydSteinberg.Draw(pm, bounds, img, image.Point{})
		g.Image = append(g.Image, pm)
		timestamps = append(timestamps, fr.timestamp)
	}
	if len(g.Image) == 0 {
		return nil, skipped, 0, toolerr.New(toolerr.CodeScreencastNotActive, "no decodable frames captured")
	}

	// Delays: gap to the next frame, in centiseconds; the last frame reuses
	// the previous delay (or 50cs for a single frame).
	total := 0
	for i := range g.Image {
		delay := 50
		if i < len(timestamps)-1 {
			delay = int((timestamps[i+1] - timestamps[i]) * 100)
		} else if i > 0 {
			delay = g.Delay[i-1]
		}
		if delay < minGIFDelayCS {
			delay = minGIFDelayCS
		}
		g.Delay = append(g.Delay, delay)
		total += delay
	}
	return g, skipped, total * 10, nil
}

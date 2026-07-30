package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
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

	// defaultScreencastMaxBytes bounds the frames held in memory. A frame
	// budget alone is a poor limit because frame size varies by an order of
	// magnitude with viewport and quality.
	defaultScreencastMaxBytes = 128 << 20
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
	if sc != nil && sc.collecting {
		img, decErr := base64.StdEncoding.DecodeString(p.Data)
		switch {
		case decErr != nil:
			sc.dropped++
		case sc.maxFrames > 0 && len(sc.frames) >= sc.maxFrames:
			sc.dropped++
			sc.collecting = false
			sc.limitHit = "maxFrames"
		case sc.bytes+len(img) > sc.maxBytes:
			sc.dropped++
			sc.collecting = false
			sc.limitHit = "memory budget"
		default:
			sc.frames = append(sc.frames, screencastFrame{data: img, timestamp: p.Metadata.Timestamp})
			sc.bytes += len(img)
		}
	} else if sc != nil && sc.active {
		sc.dropped++
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
		MaxFrames     int    `json:"maxFrames"`
		MaxDurationMS int    `json:"maxDurationMs"`
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
	maxFrames := args.MaxFrames
	if maxFrames <= 0 {
		maxFrames = maxScreencastFrame
	}
	state := &screencastState{
		active:        true,
		collecting:    true,
		filePath:      args.FilePath,
		maxFrames:     maxFrames,
		maxBytes:      defaultScreencastMaxBytes,
		maxDurationMS: args.MaxDurationMS,
	}
	m.col.screencasts[p.sessionID] = state
	// A wall-clock deadline must not depend on frames arriving: a page that
	// stops repainting sends none, so a frame-arrival check would never fire
	// and the limit was silently ignored on static pages.
	if args.MaxDurationMS > 0 {
		sess := p.sessionID
		state.stopTimer = time.AfterFunc(time.Duration(args.MaxDurationMS)*time.Millisecond, func() {
			m.col.mu.Lock()
			if sc := m.col.screencasts[sess]; sc == state && sc.collecting {
				sc.collecting = false
				sc.limitHit = "maxDurationMs"
			}
			m.col.mu.Unlock()
			// Tell Chrome to stop too, so it is not producing frames we
			// would only throw away.
			ctx, cancel := context.WithTimeout(context.Background(), defaultCallTimeout)
			defer cancel()
			_ = m.client.Call(ctx, sess, "Page.stopScreencast", nil, nil)
		})
	}
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
	sc.collecting = false
	if sc.stopTimer != nil {
		sc.stopTimer.Stop()
	}
	frames := sc.frames
	dropped := sc.dropped
	limitHit := sc.limitHit
	filePath := sc.filePath
	delete(m.col.screencasts, p.sessionID)
	m.col.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	if err := m.client.Call(callCtx, p.sessionID, "Page.stopScreencast", nil, nil); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		// Chrome only emits frames when the page paints, so a completely
		// static page yields none. Say which limit (if any) ended the
		// recording, otherwise this looks like a failure of the tool.
		msg := "no frames were captured: Chrome only sends frames when the page repaints, and this page did not"
		if limitHit != "" {
			msg += " before the " + limitHit + " limit stopped the recording"
		}
		return nil, toolerr.New(toolerr.CodeScreencastNotActive, msg).WithDetails(map[string]any{
			"truncatedBy": limitHit,
			"hint":        "interact with the page or navigate while recording, or raise maxDurationMs",
		})
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

	g, stats, err := assembleGIF(frames)
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

	// recordedMs is the wall-clock span the frames cover; gifDurationMs is
	// how long the GIF plays. They differ (a still page yields few frames
	// with long delays), and reporting only one of them was confusing.
	recordedMS := 0
	if n := len(frames); n > 1 {
		recordedMS = int((frames[n-1].timestamp - frames[0].timestamp) * 1000)
	}
	out := map[string]any{
		"path":          filePath,
		"frames":        len(g.Image),
		"recordedMs":    recordedMS,
		"gifDurationMs": stats.durationMS,
		"width":         g.Config.Width,
		"height":        g.Config.Height,
		// Always present, so "was it truncated?" never has to be inferred
		// from a missing key.
		"truncated": limitHit != "",
	}
	if dropped > 0 {
		out["droppedFrames"] = dropped
	}
	if stats.skipped > 0 {
		out["undecodableFrames"] = stats.skipped
	}
	if stats.refitted > 0 {
		// The viewport changed mid-recording; say so rather than leaving
		// the agent to wonder about the letterboxing in the output.
		out["refittedFrames"] = stats.refitted
		out["note"] = "the viewport changed during recording; frames of other sizes were drawn into the largest frame's canvas"
	}
	if limitHit != "" {
		out["truncatedBy"] = limitHit
		out["truncationNote"] = "recording stopped collecting frames when the " + limitHit + " limit was reached"
	}
	return out, nil
}

// gifStats reports what assembleGIF did with the captured frames.
type gifStats struct {
	skipped    int // undecodable frames
	refitted   int // frames whose size differed from the canvas
	durationMS int
}

// assembleGIF decodes JPEG frames, quantizes them to a 256-color palette
// with Floyd–Steinberg dithering, and derives per-frame delays from the
// capture timestamps.
//
// A recording may span a viewport change (resize_page, emulate), which
// changes the frame size mid-stream. Earlier versions dropped every frame
// that did not match the first one, so resizing during a recording silently
// reduced it to a single frame. The canvas is now the largest frame seen
// and smaller frames are drawn into it, so nothing is lost.
func assembleGIF(frames []screencastFrame) (*gif.GIF, gifStats, error) {
	var stats gifStats

	// Pass 1: header-only decode to size the canvas. Decoding every frame
	// up front would hold hundreds of full bitmaps in memory at once.
	canvas := image.Rectangle{}
	for _, fr := range frames {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(fr.data))
		if err != nil {
			continue // counted as skipped in pass 2
		}
		canvas = canvas.Union(image.Rect(0, 0, cfg.Width, cfg.Height))
	}
	if canvas.Empty() {
		return nil, stats, toolerr.New(toolerr.CodeScreencastNotActive, "no decodable frames captured")
	}

	g := &gif.GIF{Config: image.Config{
		ColorModel: color.Palette(palette.Plan9),
		Width:      canvas.Dx(),
		Height:     canvas.Dy(),
	}}
	var timestamps []float64

	// Pass 2: decode and draw one frame at a time.
	for _, fr := range frames {
		img, err := jpeg.Decode(bytes.NewReader(fr.data))
		if err != nil {
			stats.skipped++
			continue
		}
		if !img.Bounds().Eq(canvas) {
			stats.refitted++
		}
		pm := image.NewPaletted(canvas, palette.Plan9)
		draw.FloydSteinberg.Draw(pm, img.Bounds().Intersect(canvas), img, image.Point{})
		g.Image = append(g.Image, pm)
		timestamps = append(timestamps, fr.timestamp)
	}
	if len(g.Image) == 0 {
		return nil, stats, toolerr.New(toolerr.CodeScreencastNotActive, "no decodable frames captured")
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
	stats.durationMS = total * 10
	return g, stats, nil
}

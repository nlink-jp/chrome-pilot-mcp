package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nlink-jp/chrome-pilot-mcp/internal/toolerr"
)

func (m *Manager) resizePage(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Width  *int `json:"width"`
		Height *int `json:"height"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Width == nil || args.Height == nil || *args.Width <= 0 || *args.Height <= 0 {
		return nil, toolerr.New(toolerr.CodeInvalidArguments, "width and height are required positive integers")
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	err = m.client.Call(callCtx, p.sessionID, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": *args.Width, "height": *args.Height,
		"deviceScaleFactor": 1, "mobile": false,
	}, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"width": *args.Width, "height": *args.Height}, nil
}

// networkPresets mirrors the DevTools throttling presets (bytes/sec, ms).
var networkPresets = map[string]map[string]any{
	"Offline":  {"offline": true, "downloadThroughput": 0, "uploadThroughput": 0, "latency": 0},
	"Slow 3G":  {"offline": false, "downloadThroughput": 500 * 1000 / 8 * 8 / 10, "uploadThroughput": 500 * 1000 / 8 * 8 / 10, "latency": 2000},
	"Fast 3G":  {"offline": false, "downloadThroughput": 1600 * 1000 / 8 * 9 / 10, "uploadThroughput": 750 * 1000 / 8 * 9 / 10, "latency": 563},
	"Slow 4G":  {"offline": false, "downloadThroughput": 1600 * 1000 / 8 * 9 / 10, "uploadThroughput": 750 * 1000 / 8 * 9 / 10, "latency": 563},
	"Fast 4G":  {"offline": false, "downloadThroughput": 4000 * 1000 / 8 * 9 / 10, "uploadThroughput": 3000 * 1000 / 8 * 9 / 10, "latency": 165},
	"disabled": {"offline": false, "downloadThroughput": -1, "uploadThroughput": -1, "latency": 0},
}

// emulate applies the full emulation state on every call: an omitted
// parameter resets that override. extraHttpHeaders is the one exception —
// headers are deliberate, cross-navigation state, so they are only touched
// when the parameter is present.
//
// The returned `applied` map names every dimension and its effective value,
// including the ones that were reset. Reporting only what was passed made a
// reset that had not happened look like it had (a user-agent override
// survived `emulate {}` while its key vanished from the response).
func (m *Manager) emulate(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		ColorScheme       string  `json:"colorScheme"`
		CPUThrottlingRate float64 `json:"cpuThrottlingRate"`
		ExtraHTTPHeaders  *string `json:"extraHttpHeaders"`
		Geolocation       string  `json:"geolocation"`
		NetworkConditions string  `json:"networkConditions"`
		UserAgent         string  `json:"userAgent"`
		Viewport          string  `json:"viewport"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	p, err := m.selectedPage(ctx)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()
	sess := p.sessionID
	applied := map[string]any{}

	// Color scheme.
	scheme := ""
	switch args.ColorScheme {
	case "", "auto":
		// empty feature value resets the override
	case "dark", "light":
		scheme = args.ColorScheme
	default:
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "colorScheme must be dark, light, or auto, got %q", args.ColorScheme)
	}
	err = m.client.Call(callCtx, sess, "Emulation.setEmulatedMedia", map[string]any{
		"features": []map[string]any{{"name": "prefers-color-scheme", "value": scheme}},
	}, nil)
	if err != nil {
		return nil, err
	}
	applied["colorScheme"] = orDefault(scheme, "auto")

	// CPU throttling (rate 1 = off).
	rate := args.CPUThrottlingRate
	if rate <= 0 {
		rate = 1
	}
	if err := m.client.Call(callCtx, sess, "Emulation.setCPUThrottlingRate", map[string]any{"rate": rate}, nil); err != nil {
		return nil, err
	}
	applied["cpuThrottlingRate"] = rate

	// Network conditions.
	presetName := args.NetworkConditions
	if presetName == "" {
		presetName = "disabled"
	}
	preset, ok := networkPresets[presetName]
	if !ok {
		return nil, toolerr.Newf(toolerr.CodeInvalidArguments, "unknown networkConditions %q", args.NetworkConditions)
	}
	if err := m.client.Call(callCtx, sess, "Network.emulateNetworkConditions", preset, nil); err != nil {
		return nil, err
	}
	applied["networkConditions"] = presetName

	// Geolocation ({} clears the override).
	geoParams := map[string]any{}
	applied["geolocation"] = "cleared"
	if args.Geolocation != "" {
		lat, lng, err := parseGeolocation(args.Geolocation)
		if err != nil {
			return nil, err
		}
		geoParams = map[string]any{"latitude": lat, "longitude": lng, "accuracy": 1}
		applied["geolocation"] = args.Geolocation
	}
	if err := m.client.Call(callCtx, sess, "Emulation.setGeolocationOverride", geoParams, nil); err != nil {
		return nil, err
	}

	// User agent: an empty value (omitted or "") clears the override, the
	// same way an omitted viewport does.
	if err := m.client.Call(callCtx, sess, "Emulation.setUserAgentOverride",
		map[string]any{"userAgent": args.UserAgent}, nil); err != nil {
		return nil, err
	}
	applied["userAgent"] = args.UserAgent
	if args.UserAgent == "" {
		applied["userAgent"] = "cleared"
	}

	// Viewport ("WxHxDPR[,mobile][,touch][,landscape]"; omitted clears).
	if args.Viewport == "" {
		if err := m.client.Call(callCtx, sess, "Emulation.clearDeviceMetricsOverride", nil, nil); err != nil {
			return nil, err
		}
		applied["viewport"] = "cleared"
	} else {
		vp, touch, err := parseEmulatedViewport(args.Viewport)
		if err != nil {
			return nil, err
		}
		if err := m.client.Call(callCtx, sess, "Emulation.setDeviceMetricsOverride", vp, nil); err != nil {
			return nil, err
		}
		if err := m.client.Call(callCtx, sess, "Emulation.setTouchEmulationEnabled", map[string]any{"enabled": touch}, nil); err != nil {
			return nil, err
		}
		applied["viewport"] = args.Viewport
	}

	// Extra HTTP headers: the one dimension an omitted parameter leaves
	// alone, so say so explicitly rather than staying silent about it.
	applied["extraHttpHeaders"] = "unchanged"
	if args.ExtraHTTPHeaders != nil {
		headers := map[string]string{}
		if *args.ExtraHTTPHeaders != "" {
			if err := json.Unmarshal([]byte(*args.ExtraHTTPHeaders), &headers); err != nil {
				return nil, toolerr.Newf(toolerr.CodeInvalidArguments,
					`extraHttpHeaders must be a JSON object string such as {"X-Custom":"value"}: %v`, err)
			}
		}
		applied["extraHttpHeaders"] = fmt.Sprintf("%d header(s)", len(headers))
		if err := m.client.Call(callCtx, sess, "Network.setExtraHTTPHeaders", map[string]any{"headers": headers}, nil); err != nil {
			return nil, err
		}
	}

	return map[string]any{"applied": applied}, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func parseGeolocation(s string) (float64, float64, error) {
	latS, lngS, ok := strings.Cut(s, ",")
	if !ok {
		return 0, 0, toolerr.Newf(toolerr.CodeInvalidArguments, "geolocation must be <latitude>,<longitude>, got %q", s)
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(latS), 64)
	lng, err2 := strconv.ParseFloat(strings.TrimSpace(lngS), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return 0, 0, toolerr.Newf(toolerr.CodeInvalidArguments, "geolocation out of range: %q", s)
	}
	return lat, lng, nil
}

// parseEmulatedViewport parses "WxHxDPR[,mobile][,touch][,landscape]".
func parseEmulatedViewport(s string) (map[string]any, bool, error) {
	parts := strings.Split(s, ",")
	dims := strings.Split(parts[0], "x")
	if len(dims) < 2 || len(dims) > 3 {
		return nil, false, toolerr.Newf(toolerr.CodeInvalidArguments, "viewport must be WxH[xDPR][,mobile][,touch][,landscape], got %q", s)
	}
	w, err1 := strconv.Atoi(dims[0])
	h, err2 := strconv.Atoi(dims[1])
	dpr := 1.0
	var err3 error
	if len(dims) == 3 {
		dpr, err3 = strconv.ParseFloat(dims[2], 64)
	}
	if err1 != nil || err2 != nil || err3 != nil || w <= 0 || h <= 0 || dpr <= 0 {
		return nil, false, toolerr.Newf(toolerr.CodeInvalidArguments, "bad viewport dimensions in %q", s)
	}
	mobile, touch, landscape := false, false, false
	for _, flag := range parts[1:] {
		switch strings.TrimSpace(flag) {
		case "mobile":
			mobile = true
		case "touch":
			touch = true
		case "landscape":
			landscape = true
		case "":
		default:
			return nil, false, toolerr.Newf(toolerr.CodeInvalidArguments, "unknown viewport flag %q in %q", flag, s)
		}
	}
	vp := map[string]any{
		"width": w, "height": h, "deviceScaleFactor": dpr, "mobile": mobile,
	}
	if landscape {
		vp["screenOrientation"] = map[string]any{"type": "landscapePrimary", "angle": 90}
	}
	return vp, touch, nil
}

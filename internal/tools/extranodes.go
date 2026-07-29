package tools

import (
	"context"
	"fmt"
	"strings"
)

// Recovering interactive elements the accessibility tree does not expose.
//
// Chrome only surfaces an element in the accessibility tree when it has
// semantics to report. A styled <div> with a click handler but no text, no
// aria-label and no role — an icon-only button, a close box, a custom
// toggle — is therefore invisible to take_snapshot, which would leave the
// agent unable to address it at all (found in real use against a plain
// clickable div). This pass asks the DOM directly for those elements and
// appends them to the snapshot as ordinary uid targets.
//
// Cost is three CDP calls regardless of how many elements are found, and
// the whole pass is best-effort: a failure never fails the snapshot.

// extraMarkAttr is the temporary attribute used to correlate the elements
// found in JS with the node ids returned by DOM.querySelectorAll. It is
// removed again before the pass returns.
const extraMarkAttr = "data-chrome-pilot-extra"

// maxExtraNodes bounds how many recovered elements are listed, so a page
// built entirely from clickable divs cannot flood the snapshot.
const maxExtraNodes = 100

// findExtraJS marks candidate elements and describes them. Candidates are
// elements that look interactive but carry nothing the accessibility tree
// would report, so they cannot already be in the rendered tree.
const findExtraJS = `() => {
	const out = [];
	let i = 0;
	for (const el of document.querySelectorAll('*')) {
		if (el.hasAttribute('` + extraMarkAttr + `')) el.removeAttribute('` + extraMarkAttr + `');
		if (i >= ` + "MAXEXTRA" + `) continue;
		const r = el.getBoundingClientRect();
		if (r.width === 0 || r.height === 0) continue;
		let cursor = '';
		try { cursor = getComputedStyle(el).cursor } catch (e) {}
		const interactive = typeof el.onclick === 'function' || el.hasAttribute('onclick') ||
			(typeof el.tabIndex === 'number' && el.tabIndex >= 0) || cursor === 'pointer';
		if (!interactive) continue;
		// Anything with a name or a role is already reported by the
		// accessibility tree; only the nameless ones are missing.
		if ((el.textContent || '').trim() !== '') continue;
		if (el.getAttribute('aria-label') || el.getAttribute('aria-labelledby')) continue;
		if (el.getAttribute('title') || el.getAttribute('alt')) continue;
		if (el.getAttribute('role')) continue;
		const tag = el.tagName.toLowerCase();
		if (tag === 'input' || tag === 'button' || tag === 'select' || tag === 'textarea') continue;
		el.setAttribute('` + extraMarkAttr + `', String(i++));
		let cls = el.className;
		if (cls && typeof cls === 'object' && 'baseVal' in cls) cls = cls.baseVal;
		out.push({
			tag: tag,
			id: el.id || '',
			cls: String(cls || '').trim().split(/\s+/).slice(0, 2).join(' '),
			w: Math.round(r.width),
			h: Math.round(r.height),
			href: el.getAttribute('href') || '',
		});
	}
	return out;
}`

const clearExtraJS = `() => {
	for (const el of document.querySelectorAll('[` + extraMarkAttr + `]')) {
		el.removeAttribute('` + extraMarkAttr + `');
	}
	return true;
}`

type extraNode struct {
	Tag  string `json:"tag"`
	ID   string `json:"id"`
	Cls  string `json:"cls"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Href string `json:"href"`
}

// extraInteractiveNodes finds the missing elements, registers uids for them
// starting at counter, and returns the text to append to the snapshot.
func (m *Manager) extraInteractiveNodes(ctx context.Context, p *pageState, seq, counter int) (string, error) {
	var found struct {
		Result struct {
			Value []extraNode `json:"value"`
		} `json:"result"`
	}
	err := m.client.Call(ctx, p.sessionID, "Runtime.evaluate", map[string]any{
		"expression":    "(" + strings.Replace(findExtraJS, "MAXEXTRA", fmt.Sprint(maxExtraNodes), 1) + ")()",
		"returnByValue": true,
	}, &found)
	if err != nil {
		return "", err
	}
	nodes := found.Result.Value
	if len(nodes) == 0 {
		return "", nil
	}
	// Always drop the markers again, even if the correlation below fails.
	defer func() {
		_ = m.client.Call(ctx, p.sessionID, "Runtime.evaluate", map[string]any{
			"expression": "(" + clearExtraJS + ")()", "returnByValue": true,
		}, nil)
	}()

	var doc struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := m.client.Call(ctx, p.sessionID, "DOM.getDocument", map[string]any{"depth": 0}, &doc); err != nil {
		return "", err
	}
	var sel struct {
		NodeIDs []int64 `json:"nodeIds"`
	}
	err = m.client.Call(ctx, p.sessionID, "DOM.querySelectorAll", map[string]any{
		"nodeId": doc.Root.NodeID, "selector": "[" + extraMarkAttr + "]",
	}, &sel)
	if err != nil {
		return "", err
	}
	// Both lists are in document order, so they line up index for index. If
	// they somehow do not, report nothing rather than hand out uids that
	// point at the wrong elements.
	if len(sel.NodeIDs) != len(nodes) {
		return "", fmt.Errorf("extra node correlation mismatch: %d marked, %d resolved", len(nodes), len(sel.NodeIDs))
	}

	var sb strings.Builder
	sb.WriteString("\nInteractive elements not exposed in the accessibility tree\n")
	sb.WriteString("(clickable but unnamed — consider giving them an aria-label):\n")

	m.mu.Lock()
	for i, n := range nodes {
		uid := fmt.Sprintf("%d_%d", seq, counter+i)
		m.uids[uid] = uidTarget{nodeID: sel.NodeIDs[i], sessionID: p.sessionID}
		sb.WriteString("  uid=" + uid + " clickable <" + n.Tag)
		if n.ID != "" {
			sb.WriteString("#" + n.ID)
		}
		if n.Cls != "" {
			sb.WriteString("." + strings.ReplaceAll(n.Cls, " ", "."))
		}
		sb.WriteString(fmt.Sprintf("> %dx%d", n.W, n.H))
		if n.Href != "" {
			sb.WriteString(fmt.Sprintf(" href=%q", n.Href))
		}
		sb.WriteByte('\n')
	}
	m.mu.Unlock()

	if len(nodes) >= maxExtraNodes {
		sb.WriteString(fmt.Sprintf("  ... (listing capped at %d elements)\n", maxExtraNodes))
	}
	return sb.String(), nil
}

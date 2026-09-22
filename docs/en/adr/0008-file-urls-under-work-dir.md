# ADR-0008: Open file:// only inside the call's work_dir

- Status: Accepted
- Date: 2026-09-22
- Amends: [ADR-0001](0001-host-allow-block-lists.md) (how `file://` is treated, and the layer that enforces it)

## Context

ADR-0001 allowed `file://` by default (checking local HTML is the main use). `--block-local` refuses `file://` and
`data:`. It said that refusal is enforced at the tool-argument layer only, on the premise that the `Fetch`
interception sees network requests only.

An independent review on 2026-09-22 found that, by default, `navigate_page` given `file:///<home>/.ssh/id_rsa`
followed by `take_snapshot` or `evaluate_script` reads the file. It is a way around the confinement of ADR-0006 and
ADR-0007 (uploads under pathguard's Outbound policy, writes under its Local policy).

Before designing, the ways `file://` reaches Chrome were measured (2026-09-22, headless Chrome with a throwaway
profile, a fake secret file in a scratch directory, an HTTP server on 127.0.0.1).

**Ways that read the file:**

- `file://` given to `navigate_page` or `new_page`.
- JavaScript in an opened `file://` page navigating to another `file://`.
- `view-source:file://…` — the tool-argument layer passed `view-source:` as a scheme it does not know.
- A tab opened with `window.open` from a `file://` page. The interception does not reach a tab that is not attached
  yet, and selecting that tab and taking a snapshot read the file.

**Ways Chrome itself stopped:** navigation from an http page by JavaScript, a link or a 302 redirect; navigation from
`data:` or `about:blank`; an iframe in an http page; `fetch(file://…)` from a `file://` page.

**On the interception:** `Fetch` does see `file://` — ADR-0001's premise was wrong. The page itself, the CSS and
images it loads, navigation by JavaScript, `view-source:` and iframes all reached `Fetch.requestPaused`. With the
pattern `file://*`, no http request paused at all, and `view-source:file://` paused under it as the `file://` request
inside.

## Decision

- `file://` opens only when it lies inside the `work_dir` the call names and passes pathguard's Local policy.
  `navigate_page` and `new_page` gain `work_dir` (the argument, else `_meta`), resolved only when the URL is `file://`.
  Outside `work_dir` is `path_not_allowed` (`outside_work_dir`); a credential location and the like are refused with
  pathguard's reason. The place is the last of pathguard's forms (every link followed, a dangling one by its
  target), so whether the file exists does not change the answer.
- A `work_dir` allowed this way is recorded as a grant to that page's session. Grants are per session and accumulate.
- The `Fetch` interception is always on. With no host restriction its pattern is `file://*` only (no round trip on
  http browsing); with one, `*` as before. A `file://` request continues only when it lies inside one of the
  session's grants and passes Local; anything else fails with `BlockedByClient`. Navigation by JavaScript,
  subresources, iframes and `view-source:` stop here.
- The tool-argument layer judges `view-source:` by the URL inside it.
- A page showing a local file no grant covers (a tab opened with `window.open`, a tab the user opened) may be used by
  no tool but `navigate_page`. The tools that use a page go through `selectedPage`, which checks the page's current
  URL each time — **before** attaching it (attaching turns the console and network collectors on). Navigating away
  with `navigate_page` makes it usable again. In attach mode a user's tab is not blanked behind their back.
- The three tools that do not go through `selectedPage` follow the same rule. `get_network_request` and
  `get_console_message`, which read collected records by id, refuse when the record's page shows a local file no
  grant covers, and `get_network_request` also refuses an ungranted local-file load itself (its body is that file).
  `handle_dialog` still answers the dialog (the page is not left blocked) but returns no words on such a page.
- A URL with control characters is refused. Chrome drops leading and trailing controls and spaces, and tabs and
  newlines anywhere, before it parses a URL, so `fi\tle:///…` is a local-file URL to it.
- The interception's decision does not take `m.mu`: some paths call CDP while holding it, and a call waiting on a
  paused `file://` load would deadlock against it. `protectedPlaces` reads the driven profile's location under a lock
  of its own, and the resolver is built at the moment of use (as in ADR-0007).
- `--block-local` still refuses every `file://` and `data:`. `data:` is unchanged (Chrome stops navigation from
  `data:` to `file://`).

## Alternatives considered

| Alternative | Why not |
|---|---|
| Refuse `file://` by default, with a flag to allow it | ADR-0001's main use (checking local HTML) would not work by default, and setting the flag brings back the original hole (anything readable) |
| No `work_dir`; judge by the Local policy alone | Local passes ordinary files under the home (`~/Documents` and the like). Without a root, JavaScript in an opened page could navigate anywhere |
| `Target.setAutoAttach` with `waitForDebuggerOnStart`, stopping a new tab before it loads anything | The strongest, but it rebuilds the on-demand attaching the page management rests on. Reads can be stopped at their one entrance, so it is not taken now and is kept as a way to strengthen this |
| Judge by the Outbound policy | The page's content does reach the model, but it is treated like the other servers' reads (Local). A `file://` page cannot read another file (`fetch` fails; an iframe is another origin), so all it could send out is its own content |

## Consequences

- **Behaviour change**: a `file://` outside `work_dir` no longer opens. Opening `file://` needs `work_dir` (argument or
  `_meta`). `view-source:file://` follows the same rule.
- A page showing a local file no grant covers can be touched only with `navigate_page`.
- `Fetch` is always on with `file://*`. It does not affect http browsing (measured).
- Remaining limits: JavaScript in an HTML file inside a granted `work_dir` can send that file's own content out (the
  same egress limit as ADR-0001). The interception does not reach a tab not yet attached, but reads stop at their
  entrance. Workers a granted HTML file starts are not measured (the interception is installed on page sessions
  only). `chrome:`, `devtools:`, `blob:` and `filesystem:` are left to the browser as before (none reads an
  arbitrary path).
- A grant covers everything under `work_dir`. A broad directory (`~/Documents`, say) opens all of it — name the
  narrowest one.
- ADR-0001's "`file://` / `data:` are enforced at the tool-argument layer only" is amended for `file://`.

## References

- ADR-0001 (host allow/block lists), ADR-0006 (writes and uploads stay under `work_dir`), ADR-0007 (pathguard)
- Organization ADR-021 (the work-dir contract), ADR-022 (one path judgement)

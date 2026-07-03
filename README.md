<p align="center">
  <img src="docs/karmax.png" alt="KARMAX logo" width="96" />
</p>

<h1 align="center">karmax-loops</h1>

The public marketplace registry for [KARMAX](https://github.com/MelloB1989/KARMAX) loops —
recurring automations (news digests, chat sweeps, watchers, syncs) that run inside the
KARMAX personal AI daemon.

**Browse:** https://mellob1989.github.io/karmax-loops/ · or `karmax loops browse`

## Loops in this registry

**Default** (pre-installed with KARMAX): `tech-news`, `hot-sync`, `profile-refresh`,
`daily-briefing`. **Optional**: `chat-sweep`, `act-on-pending`, `memory-bootstrap`,
`gchat-watch`, `cold-scan`, `hn-digest` — install with `karmax loops install <name>`.

## How it works

- Every loop is a directory under [`loops/`](loops/) with a `loop.json` manifest
  (name, description, version, author, tags, schedule, config keys).
- **Registry-hosted loops** keep their Go code right here, next to the manifest —
  no repo of your own needed. This repo is a Go module; KARMAX installs a loop with
  `go get` + a blank import and a rebuild.
- **External loops** live in the author's own module; only the manifest is here
  (`"module": "github.com/you/karmax-my-loop"`).
- The website is static (GitHub Pages, [`docs/`](docs/)) and reads `loops/*/loop.json`
  live from this repo — merging a PR is deploying.

## Install a loop

```bash
karmax loops browse            # see what's available
karmax loops info hn-digest    # details
karmax loops install hn-digest # go get + import + rebuild + restart
```

## Publish a loop

```bash
karmax loops new my-loop       # scaffold: loop.go + loop.json + README
# implement Run() in loop.go — the loopkit.Kit gives you the daemon's powers:
#   Ask (main agent) · Harness (Claude Code) · Remember/Recall (memory)
#   Notify (app push) · Propose (approvals inbox) · Remind (phone reminder)
#   SendWhatsApp/ReadWhatsApp · HTTP · Config · RunLoop · Trigger · Logf
karmax loops publish my-loop   # validates + compiles, then opens a PR here
```

`publish` commits directly if you have write access, otherwise it forks and opens a
pull request automatically (needs the [`gh` CLI](https://cli.github.com/)). To host
the code in your own repo instead, scaffold with
`karmax loops new my-loop --module github.com/you/karmax-my-loop`.

### Manifest reference (`loop.json`)

```jsonc
{
  "name": "my-loop",             // kebab-case, unique in the registry
  "description": "One line shown in the marketplace.",
  "version": "0.1.0",
  "author": "your-github-login",
  "module": "",                  // empty = code lives in this repo under loops/<name>/
  "package": "",                 // import path override (defaults sensibly)
  "repo": "",                    // human link to the code (external loops)
  "tags": ["news"],
  "schedule": "0 0 9 * * *",     // informational; the loop registers its own schedule
  "config": [                    // install-time env keys: KARMAX_LOOP_<NAME>_<KEY>
    { "key": "api_key", "description": "…" }
  ]
}
```

### Review bar

PRs are reviewed for: a truthful description, code that only uses the `loopkit.Kit`
surface plus stdlib/public APIs, no secrets in code (use `k.Config`), and decisions
routed properly (`Propose` for anything needing operator approval, `Remind` for
operator-only actions, `Notify` for information).

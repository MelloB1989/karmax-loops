# hn-digest

The marketplace's reference loop: every morning at 09:00 it fetches the Hacker News
top stories, summarizes them through the Claude Code harness, saves the digest to
your KARMAX memory, and pushes it to the phone app.

```bash
karmax loops install hn-digest
```

No configuration required. Use this loop as a template: `karmax loops new my-loop`
scaffolds the same shape.

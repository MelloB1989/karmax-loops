# tech-news

**Default loop.** Every morning at 09:00 it sends the Claude Code harness to research
the last 24–48h of AI / dev-tooling / startup / security news on the live web, distills
5–8 one-line items with sources, and saves the digest to KARMAX's long-term memory —
where the `daily-briefing` loop picks it up.

Runs on the Claude subscription (independent of the main model), so it keeps working
even when the main model is rate-limited. No configuration.

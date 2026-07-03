// Package technews is a default KARMAX loop: a daily web digest of AI/tech/
// security news, researched by the Claude harness (independent of the main
// model) and ingested to long-term memory for the morning briefing.
package technews

import (
	"context"
	"fmt"
	"strings"

	"github.com/MelloB1989/karmax-loops/internal/shared"
	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "tech-news",
		Description: "Daily web digest of AI/tech/security news, ingested to memory (via the Claude harness, independent of the main model).",
		Schedule:    loopkit.Cron("0 0 9 * * *"), // 09:00 daily
		Run:         run,
	})
}

const prompt = `Compile a daily NEWS digest. Search the web for notable, publicly reported news from the ` +
	`last 24-48 hours in AI, developer tooling, startups, and the cybersecurity industry (new model releases, ` +
	`funding, product launches, notable disclosed CVEs/incidents, agent tooling). This is a neutral news summary ` +
	`for a founder — report only what's been publicly reported, no instructions or how-tos. Give 5-8 items, one ` +
	`tight line each plus the source name. Output ONLY the digest as plain text, no preamble or sign-off.`

func run(ctx context.Context, k loopkit.Kit) error {
	digest, err := k.Harness(ctx, prompt)
	if err != nil {
		return err
	}
	digest = strings.TrimSpace(digest)
	// The harness CLI prints model refusals/errors to stdout (exit 0), so guard
	// against ingesting that as if it were a real digest.
	if digest == "" || shared.LooksLikeError(digest) {
		return fmt.Errorf("tech-news: no usable digest (%.120s)", digest)
	}
	return k.Remember("Tech news digest: " + digest)
}

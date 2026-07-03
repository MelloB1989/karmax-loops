// Package profilerefresh is a default KARMAX loop: it rewrites the curated
// ABOUT_ME profile from recent memory so the agent's model of the operator
// stays current.
package profilerefresh

import (
	"context"

	"github.com/MelloB1989/karmax/pkg/loopkit"
)

func init() {
	loopkit.Register(loopkit.Loop{
		Name:        "profile-refresh",
		Description: "Rewrites the ABOUT_ME profile from recent memory.",
		Schedule:    loopkit.Every("12h"),
		Run:         run,
	})
}

const prompt = `Retrieve recent context from memory, read the current ABOUT_ME.md profile, and rewrite ` +
	`it with profile.update so it reflects the latest truth about your operator. Preserve facts that are still valid.`

func run(ctx context.Context, k loopkit.Kit) error {
	_, err := k.Ask(ctx, prompt)
	return err
}

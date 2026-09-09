# lyzn-tasks

Does the things you promised out loud.

LYZN is a pendant and a phone app: it hears a conversation, notices the
commitments in it, and shows them to you. When you approve one, somebody still
has to do it — and LYZN cannot, because the work is your shell, your files and
your signed-in accounts. This loop is that half, running on your machine.

It is the workflow-tier build of [`recipes/lyzn-tasks.yaml`](../../recipes/lyzn-tasks.yaml).
Reach for the recipe first — it is one file you can read, with no toolchain and
no restart. Reach for this when you want the daemon to speak LYZN's documented
API exactly, which is what it does today: the recipe needs three small additions
to that API before it works, and this does not.

## How the work gets here

Nothing is ever pushed at it. A laptop behind a home router has no address LYZN
could reach, and karmax-desktop pins the KARMAX API to `127.0.0.1`, so the
schedule in `loop.yaml` *is* the connection:

```
every 2 minutes:
  POST /daemons/heartbeat      → {"ok":true,"tasks":n}   ← keeps the app's dot lit
  if n == 0: stop                                        ← an idle laptop costs one request
  GET  /daemons/work           → the approved queue, oldest first
  for the first three:
    POST /daemons/work/:id/claim     409 → someone else has it, move on
    harness(brief)                   ← the work itself, synchronously
    POST /daemons/work/:id/result    idempotent; the receipt is printed here
```

## Whose model, whose key

Not this loop's. The harness runs on whatever you already configured — your own
Claude Code or Codex subscription — and nothing in this module names a model, a
base URL or a key for it. That is the Act plan, and it does not change under Act
Pro: which endpoint the harness talks to stays the harness's configuration.

## The contract with the harness

Every brief ends with two lines:

```
STATUS: done|blocked|failed
SUMMARY: <one sentence>
```

They are read by `parseOutcome`, and the default is failure. A coding harness
that refuses, runs out of turns, or answers the question instead of doing the
work still exits 0 with prose on stdout — so a reply with no contract line is
not an ambiguous result, it is a result there is no evidence for. Only
`STATUS: done` produces `outcome: success`, which is the only thing that prints
a receipt stamped **DONE**.

`blocked` is kept apart from `failed` on this side even though LYZN records both
as a failure. Blocked work is finished except for the one thing only you have —
a password, an account, a decision — so it arrives as an approval in your inbox
rather than as a notification you can only read.

## Configuration

```bash
KARMAX_LOOP_LYZN_TASKS_TOKEN=…    # the 43-character daemon token from pairing
KARMAX_LOOP_LYZN_TASKS_API=…      # optional; defaults to https://api.lyzn.ai
```

Without a token it says so once per run and does nothing else. Pairing is in
[`docs/lyzn.md`](../../docs/lyzn.md).

Pointing `API` somewhere else also needs that host in `capabilities:`, which
means re-signing and re-approving — as changing where a daemon reports should.

## Building it

You cannot build it in a fresh clone of this repo. `workflows/go.mod` pins the
guest SDK with

```
replace github.com/MelloB1989/karmax => /home/nikhil/code/KARMAX
```

which is a path on the author's machine. With a KARMAX checkout of your own,
point a `go.work` at it (do not commit one) and then:

```bash
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o lyzn-tasks.wasm ./lyzn-tasks
karmax wloop sign --manifest lyzn-tasks/loop.yaml --module lyzn-tasks.wasm
```

## Testing it

```bash
cd workflows && go test ./lyzn-tasks/
```

Everything it decides — which tasks to claim, what to tell the harness, and what
the harness's answer meant — is pure and lives in `decisions.go`, with no build
tag on it, so the tests run on an ordinary machine with no WASM, no daemon and
no LYZN account.

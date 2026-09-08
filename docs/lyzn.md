# Connecting a laptop to LYZN

LYZN is a pendant and a phone app. It hears a conversation, notices the
commitments in it, and shows them to the person who made them. When they
approve one, somebody still has to do it — and LYZN cannot, because the work is
a shell, a filesystem and a set of signed-in accounts. KARMAX is where those
are.

So the two halves meet like this: **the laptop asks.** LYZN can never call a
machine behind a home router, and karmax-desktop pins the KARMAX API to
`127.0.0.1`, so there is no inbound path at all. A loop's schedule is the whole
connection — every two minutes it says it is alive, asks what has been
approved, claims a task, does it through the coding harness, and posts the
outcome back so LYZN prints the receipt.

Two builds of the same loop:

| | [`recipes/lyzn-tasks.yaml`](../recipes/lyzn-tasks.yaml) | [`workflows/lyzn-tasks/`](../workflows/lyzn-tasks) |
|---|---|---|
| install | drop the file in; live in three seconds | verify, approve, restart |
| needs | three additions to LYZN's daemon API — see below | nothing; it speaks the documented API today |
| edit it | it is YAML, in your own editor | Go, a wasm toolchain, and a signature |

**Your model and your key are yours.** Neither build names a model, an endpoint
or a key for the harness. It runs on whatever you already configured — your own
Claude Code or Codex subscription — and that stays true under Act Pro: which
API the harness talks to is the harness's own configuration, and nothing in
this registry decides it.

---

## Pairing

**1. Get the code.** In the LYZN app: **Settings → Pair a laptop**. It shows six
characters (`K7QD2M`) and they are good for five minutes and one use.

**2. Run this, with that code.** It redeems the code, takes the recipe from this
registry, writes your token into its one editable line, and puts the result
where KARMAX is watching:

```bash
CODE=K7QD2M   # ← from the app

TOKEN=$(curl -fsS -X POST https://api.lyzn.ai/daemons/claim \
  -H 'Content-Type: application/json' \
  -d "{\"code\":\"$CODE\",
       \"name\":\"$(hostname -s)\",
       \"hostname\":\"$(hostname)\",
       \"os\":\"$(uname -s)/$(uname -m)\",
       \"version\":\"$(karmax version 2>/dev/null | head -1)\",
       \"capabilities\":[\"claude-code\",\"shell\"]}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')

mkdir -p ~/.karmax/recipes
curl -fsSL https://raw.githubusercontent.com/MelloB1989/karmax-loops/main/recipes/lyzn-tasks.yaml \
  | sed "s|in: \[\"\"\]|in: [\"$TOKEN\"]|" > ~/.karmax/recipes/lyzn-tasks.yaml
chmod 600 ~/.karmax/recipes/lyzn-tasks.yaml
```

The token is 43 characters and is **shown once** — LYZN stores only its SHA-256,
so a lost token means pairing again. `chmod 600` is not decoration: that file
now holds a credential, and it is the reason this recipe should not be shared
the way the others can be.

There is no restart and no install step. KARMAX polls `~/.karmax/recipes/` every
three seconds; the file is live before you have finished reading this sentence.

**If you would rather not paste a script**: do step 2 by hand. Redeem the code,
copy `recipes/lyzn-tasks.yaml` into `~/.karmax/recipes/`, and change the one
line that says

```yaml
      in: [""]
```

to hold your token. That line is marked in the file and it is the only thing in
it you ever edit — a recipe has no way to read a setting, so it lives there.

---

## What happens next

You approve a commitment in the app. Within two minutes this laptop claims it,
so a second paired laptop cannot start the same job, and hands it to the coding
harness with the task, the sentence you actually said, the due date, and what
the conversation was about. When the harness finishes, the whole of its reply
goes back to LYZN, which reads the two contract lines it was asked to end with
and prints the receipt — **DONE** or **FAILED**, both of them, because a record
that keeps only the wins proves nothing.

If the harness got stuck on something only you can give it — a password, an
account, a decision — you get a notification on this machine as well as the
receipt on your phone.

## Telling whether it works

```bash
karmax recipe check lyzn-tasks     # the file parses and the schedule is real
karmax recipe list                 # it is loaded, with its trigger and step count
karmax loops run lyzn-tasks        # do not wait for the cron; run it now
```

Then look in three places:

- **The app**, under Settings → paired laptops: this machine, with a live dot.
  LYZN calls a daemon offline after 90 seconds of silence and the recipe beats
  every two minutes, so the dot blinks. Change the schedule to `"0 * * * * *"`
  if that bothers you.
- **The KARMAX log**, which carries every line the recipe writes. An unpaired
  laptop says so once per run and does nothing else:
  `lyzn-tasks: this laptop is not paired with LYZN…`
- **A receipt in the app** after the first task goes through.

When somebody unpairs this machine in the app, the very next request is a `401`,
the run fails, and the log says which call was refused. Pair again to fix it —
the old token cannot come back.

---

## What the recipe still needs from LYZN

`backend/docs/daemon-api.md` in the LYZN repo is the contract, and the workflow
build speaks it exactly. The recipe cannot, for one reason: **a KARMAX recipe
can read a JSON array of scalars and nothing else.** It has no way to reach into
an object, so `{"tasks":[…]}` is not something it can walk, and `{"outcome":…}`
is not something it can compose. Three additions fix that. All three are
additive — no documented behaviour changes.

**1. Ids, as an array.**

```
GET /daemons/work?format=ids
→ 200  ["rec_1-0", "rec_3-2"]        # oldest first, same cap, same auth
→ 200  []                            # nothing waiting
```

`foreach` walks that. Without it the recipe stops at "…is not a JSON list" and
claims nothing, which is the right way to fail.

**2. The claim answers with the whole task.** `POST /daemons/work/:taskId/claim`
already returns `{"task": …}`; that task should carry the fields the work poll
carries — `text`, `quote`, `dueAt`, `kind` and `context` — and not only the
stored row. The recipe never sees the poll's body, so the claim is its only
chance to learn what the promise was. (It is better API design anyway: a claim
that tells you less than the poll did makes the poll compulsory.)

**3. The result endpoint speaks plain text, both ways.**

```
POST /daemons/work/:taskId/result
Content-Type: text/plain

<the harness's entire reply, verbatim>
```

LYZN reads the last `STATUS:` and `SUMMARY:` lines — the two the brief demands —
and decides: `STATUS: done` is `outcome: success`, and **anything else, missing
or unreadable, is a failure**. That is the same rule the document already states
one layer up, applied where the evidence is.

The answer is `text/plain` as well:

```
→ 200  (empty body)                              # nothing needs a person
→ 200  Drive is signed out — sign in and it goes. # one sentence, shown to the operator
```

Empty or not is the only branch a recipe has, and this is what turns it into
"tell them" or "say nothing". JSON in still means JSON out, unchanged, which is
what the workflow build uses.

Until all three are live, a paired laptop fails the run at its second request
and claims nothing. Do not put the recipe on a machine before then — a claimed
task that never gets a result stays claimed.

---

## Publishing it

Nothing here is published yet. When it is:

**The recipe** — it is read as data, so the review is the diff. Add the entry to
`index.json`:

```json
{
  "name": "lyzn-tasks",
  "kind": "recipe",
  "version": "1.0.0",
  "description": "The commitments you approved in the LYZN app, carried out on your own laptop.",
  "author": "MelloB1989",
  "source": "recipes/lyzn-tasks.yaml",
  "artifact": "recipes/lyzn-tasks.yaml",
  "sha256": "66ac6a5ec32c3cab83b5cef272b16546f4a90b2ad0b35d1af899d2304b8eb0ae"
}
```

The digest is of the file exactly as committed:

```bash
shasum -a 256 recipes/lyzn-tasks.yaml
./scripts/build-index.py        # regenerates the whole index from the tree
```

`karmax loops browse` reads `index.json` directly, so **merging is publishing** —
which is why the entry above is written down here rather than merged.

**The workflow** — needs a KARMAX checkout, because `workflows/go.mod` pins the
guest SDK to `/home/nikhil/code/KARMAX`, a path on the author's machine. Point a
`go.work` at your own checkout (do not commit it), then:

```bash
cd workflows
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o lyzn-tasks.wasm ./lyzn-tasks
karmax wloop sign --manifest lyzn-tasks/loop.yaml --module lyzn-tasks.wasm
./scripts/build-index.py --artifacts ./dist    # pins it by digest
```

Attach the `.kloop` to a GitHub Release — artifacts are never committed — and
open the PR with the source, `loop.yaml` and the index entry. Until the registry
key countersigns it, installing takes `--untrusted` and typing the loop's name.

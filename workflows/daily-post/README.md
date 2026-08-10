# daily-post

Writes one post about your day and publishes it to X and LinkedIn. Nobody reads
it before it goes out.

That last sentence is the whole design. Everything below exists because a post
cannot be unpublished — it has already been seen, and the person it named did
not choose to be written about.

## Try it first

```bash
karmax social dry-run on
karmax loops run daily-post
```

Every post it would make arrives on your WhatsApp instead, marked
`✅ would post` or `🚫 refused`. It works before either account is connected —
previewing is what you do before signing in.

A run you ask for ignores the once-a-day rule, so you can iterate. On a quiet
day it will normally decline; `KARMAX_LOOP_DAILY_POST_FORCE=true` makes it
write anyway. That skips the "is today worth it" check and nothing else — not
the privacy guard, not the rate limit.

## What it reads

| Source | What it takes | What it does not take |
|---|---|---|
| `activity.recent` | The engineering tasks KARMAX ran for you, and whether they finished | The harness output — full of paths, hostnames and occasionally secrets |
| `google_workspace` | Today's calendar event **titles** | Descriptions and attendee lists, the two fields most likely to name somebody |
| `whatsapp_search_messages` | **Your own outgoing messages**, `from_me: true` | Anything anyone said to you |
| `recall` | What long-term memory holds about your current work | — |

The WhatsApp rule is the one worth stating twice. Writing a public post from
other people's messages means publishing, in paraphrase, things they said in
private to one person. Your own words are yours to reuse. Nobody else's are.

This loop has no send tool. It reads WhatsApp and cannot use it — all WhatsApp
automation belongs to `wa-monitor`.

## What stops it saying the wrong thing

Three layers, in the order they apply:

1. **The material is stripped** before the model sees it. `@handles` go, and so
   does any capitalised word following "with", "from", "met", "call". A model
   that never reads the name does not have to be trusted not to write it.
2. **The brief states the rules** — no names, no money, nothing unannounced,
   write about what you did and thought rather than who you did it with. This
   is a request, and requests are not guarantees.
3. **The host checks the result**, and this is the one that counts. `x.post` and
   `linkedin.post` refuse any text containing a name from your contacts or your
   memory, an amount of money, a phone number, an email address, a WhatsApp id,
   an internal URL, or anything shaped like a credential. That check is in
   KARMAX. This loop cannot argue with it, and neither can a modified copy of
   this loop.

Refused drafts are recorded too — `karmax social log` shows what it tried to
say and why it was stopped.

## What stops it being annoying

- **Nothing on an ordinary day.** A day needs four signals to clear the bar;
  shipped work counts double, a day of three short replies counts for nothing.
- **Once per platform per day**, at most two posts a day, three hours apart
  (`karmax social` shows the current count).
- **No repeats.** Posts are compared by content-word overlap over three weeks,
  not by string — the same observation reworded is still a repeat.
- **It may decline.** If the day contains nothing publishable under the rules,
  the model replies `SKIP` and nothing goes out. That is a correct answer.

## Switching it off

```bash
karmax social off "on holiday"   # takes effect on the next post, no restart
karmax social on
karmax social                    # what it has posted, and whether it is on
karmax social log                # what it said, and what it was stopped from saying
```

`KARMAX_SOCIAL_POSTING=off` in the environment does the same thing, for a
machine rather than an account.

## Configuration

```bash
KARMAX_LOOP_DAILY_POST_PLATFORMS=x        # default "x,linkedin"
KARMAX_LOOP_DAILY_POST_MIN_HOUR=17        # earliest hour it will post
KARMAX_SOCIAL_PER_DAY=2                   # host-side, applies to all posting
KARMAX_SOCIAL_MIN_GAP=3h
```

## Connecting the accounts

```bash
karmax login x           # four values from the X developer portal (Read and write)
karmax login linkedin    # browser sign-in; the app needs the w_member_social scope
```

X uses OAuth 1.0a rather than OAuth 2.0, which looks backwards and is not:
X's OAuth 2.0 client-credentials token is app-only, and app-only cannot post as
a user.

## Testing it

```bash
go test ./daily-post/
```

Everything it decides — whether a day is worth writing about, what gets
stripped, whether a draft is a repeat — is pure and tested with no WASM, no
daemon and no social account.

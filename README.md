# statable

Statable analytics from the command line: a terminal, a script, or CI.

Sixteen commands over the [Stats API v1](https://statable.com/docs/developers/stats-api/).

## Install

**Arch, and Omarchy**

```bash
yay -S statable-bin
```

**macOS and Linux, with Homebrew**

```bash
brew install key-arg/tap/statable
```

**Anywhere with a Go toolchain**

```bash
go install github.com/key-arg/statable-cli/cmd/statable@latest
```

**mise**

```bash
mise use -g ubi:key-arg/statable-cli
```

**By hand.** Download the archive for your platform from the
[releases page](https://github.com/key-arg/statable-cli/releases) and put the
binary on your `PATH`. Each release also carries `checksums.txt`, signed
keylessly, so you can check what you downloaded against who built it:

```bash
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github\.com/key-arg/statable-cli/\.github/workflows/.+' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check checksums.txt --ignore-missing
```

There is deliberately no `curl … | sh` installer. Piping a URL into a shell
asks you to run whatever the server sends today, and it is the single loudest
complaint the Omarchy project has had to answer for.

### Completions and man pages

The Homebrew cask and the AUR package install both. Installing by hand, they
are in the archive under `completions/` and `manpages/`. `go install` builds
only the binary, so generate the completion yourself:

```bash
statable completion zsh > "${fpath[1]}/_statable"
statable completion bash > /etc/bash_completion.d/statable
statable completion fish > ~/.config/fish/completions/statable.fish
```

## Use

```bash
statable auth login                 # prompts on a terminal, prints instructions anywhere else
statable sites                      # what this key can read
statable sites use example.com      # remember a default

statable now                        # visitors active right now
statable stats --range 30d --compare previous
statable series --by day --range 30d
statable top pages
statable top countries --range month
```

`top` takes a short name (`pages`, `sources`, `countries`, `browsers`, `goals`, …)
or a full dimension such as `visit:utm_campaign`.

Two of those dimensions return no comparison at all, whatever `--compare` says:
`codes` and `goals`. The rows come back without previous figures rather than
with zeroes.

The rest of the surface:

```bash
statable props                      # custom property keys, and the event each belongs to
statable funnels                    # saved funnel definitions
statable funnel 45 --range 7d       # run one, step by step
statable subscription               # the plan state of the key's owner
```

A funnel's conversion rate is cumulative: it is measured against everyone who
entered, not against the step before. Dropoff is the opposite and counts
against the previous step alone.

For CI:

```bash
statable check --metric visitors --range 7d --min 100
statable check --metric bounce_rate --max 60
```

The exit code is the answer: 0 within bounds, 3 outside them, and the usual 1
or 2 when the check could not run at all. That third code is the point — a
pipeline can tell "traffic dropped" apart from "the tool is broken".

For anything the short commands do not cover:

```bash
statable query --metric visitors,pageviews --range 7d
statable query --metric visitors --dimension time:day --filter country=US
statable query --metric visitors --dimension event:props:plan --filter event=Signup
```

The escape hatch reaches every endpoint, including ones no command wraps, and
prints the response exactly as the server sent it:

```bash
statable api call GET /sites
statable api call POST /query --var site_id=123 --var metrics='["visitors"]' --var date_range=7d
```

`--var` values are parsed as JSON, so `--var limit=25` sends a number and
`--var metrics='["visitors"]'` sends an array. `--raw-var` always sends a
string, which is how you send the literal text `25`. Guessing between the two
is the ambiguity these flags exist to remove. `--allow-errors` prints the body
of a 4xx or 5xx instead of refusing to show it, while the exit code still tells
the truth.

The CLI can also teach its own surface, reading the API's own description:

```bash
statable api search            # a table of contents
statable api search funnels
statable api describe /query   # what it takes, required fields first
```

That document is cached for a day, so exploring costs nothing against the
hourly budget.

Filters read like the question they ask. `=` is, `!=` is not, `~` contains,
`!~` does not contain. Commas inside one filter are OR; separate `--filter`
flags are AND, which is exactly how the API models them.

```bash
statable top pages --filter country=US,DE --filter page~/blog
```

## Design rules

These are enforced by tests, not by convention. Breaking one is a release
blocker.

**Print and exit.** There is no full-screen interface. If a capability existed
only inside a TUI, it would be a bug for every CI job, every pipe and every
agent.

**Machine output cannot be corrupted.** When `--format` is `json` or `csv`, the
writer that human-facing text goes to is `io.Discard`. A forgotten `Println`
writes nothing rather than breaking a payload.

**stdout is the payload.** Notes, warnings and progress go to stderr in every
format, so `statable ... --json | jq` is always valid input. Help is prose, so
in a machine format it goes to stderr too, and a bare invocation is a usage
error rather than help on stdout and a success code.

**A chart is a reading aid, not data.** The sparkline in `series` appears only
on a terminal, for the same reason the table header does: in a pipe it would be
a last line no parser expects. The scale starts at zero, so a flat week looks
flat rather than dramatic.

**Geographic breakdowns answer twice.** A person sees "United States"; a script
gets `US`, because the code is exactly what the matching filter accepts, so a
breakdown value drops straight into the next query.

**Every format carries the same content.** A command never reaches for JSON
directly; it emits a record or a table and the writer encodes it. Asking for
CSV and silently receiving JSON is a broken contract for anything piping into
`cut`.

Two things deliberately sit outside the payload. A failure is always a JSON
envelope in a machine format, because an error is not tabular. And facts about
how to *read* the numbers — the period the server resolved, that a window
overlaps imported data, that more rows exist — go to stderr in every format,
because they are not rows and would not survive being made into one.

**Nothing blocks where nobody can answer.** How the process was invoked is
resolved once, and every interactivity decision is a pure function of that.
Without a terminal, `auth login` prints what to do next and exits 1 instead of
waiting for input forever.

**Exit codes mean something.**

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Action required; `next_steps` names the fix |
| 2 | Failure |
| 3 | `statable check` ran and the metric was outside its bounds |
| 64 | The command line itself was malformed |
| 130 | Interrupted |

Code 64 covers every mistake caught before a request exists: an unknown metric,
a filter with no operator, a range that is not a range, a limit outside what the
API accepts. Nothing was sent anywhere, so repeating the command unchanged will
fail identically. That is the distinction 2 exists to make impossible to
confuse: a typo is not an outage.

**Errors are branchable.** Failures carry a stable `code`. Prose may be
reworded at any time; codes may not.

```json
{
  "status": "action_required",
  "error": "no API key is configured",
  "issues": [{"code": "NOT_AUTHENTICATED", "message": "no API key is configured"}],
  "next_steps": ["statable auth login"]
}
```

**The site listing is cached, authenticated, and repairs itself.** The API needs a numeric
site id and people name sites by domain, so every reading command used to list
the sites first and spend two calls an hour where one would do. The listing is
kept for a day in a `0600` file, carrying a MAC computed under the key and the
server address. That is not decoration: the file maps a site *name* to a site
*id*, so anything that could rewrite it could point a name at someone else's
property and have the command report on it with exit 0 and no warning. A value
merely derived from the key would sit in the same file and could be copied
across; a MAC cannot be produced without the key, so a rewritten file is simply
a miss. Nothing is cached at all when there is no home directory, because the
fallback location is world-writable.

When the server disagrees with a cached id — a site renamed or recreated — the
cache is dropped, the listing refreshed and the call retried once. `statable
sites` always asks the server, which makes it the repair anyone can run by
hand.

**Server text can never forge a line.** A note carries a site name, a funnel
name, a path — text this program did not write. Notes and warnings sanitise
what they print, so a funnel named with an embedded newline cannot add a second
line an operator would read as real, and an escape sequence in a name cannot
colour the rest of the session.

**Credentials never degrade silently.** The key goes to the system keyring. If
the keyring is unavailable, the command refuses and names `--insecure-storage`
rather than quietly writing the key in the clear. `statable auth status` always
reports which source the active key came from.

Resolution order: `--key`, then `STATABLE_API_KEY`, then the keyring, then a
`0600` file. The credential is resolved on first use, so `statable version`
never waits on a locked keyring, and every keyring call is bounded and
cancellable.

The environment variable is suggested before `--key` everywhere, because a key
on the command line is visible in `ps` and in shell history, and the headless
path is exactly where that matters.

**A project file cannot redirect the API.** `.statable.yml` may set `site` and
`period` — the default range when `--range` is not given — and nothing else. It lives in a repository, so it is written by
whoever can open a pull request; a file that could set the API host or a token
would be a supply-chain hole. Refused keys are reported on stderr rather than
ignored.

## Environment

| Variable | Effect |
|---|---|
| `STATABLE_API_KEY` | The key to use |
| `STATABLE_API_URL` | Override the API base URL. Environment only, never the project file |
| `STATABLE_CONFIG_DIR` | Where credentials and `settings.json` live. Defaults to `$XDG_CONFIG_HOME/statable` |
| `STATABLE_FORMAT` | Default output format |
| `STATABLE_AGENT` | Declare an agent harness so the CLI never prompts |
| `NO_COLOR`, `FORCE_COLOR`, `CLICOLOR`, `CLICOLOR_FORCE` | Standard colour control |
| `STATABLE_NO_INPUT` | Never prompt, the same as `--no-input` |
| `BROWSER` | Consulted when deciding whether a URL could reach a human |

## Develop

```bash
go test ./...
go test -race ./...
```

The truth table in `internal/execctx` checks all 768 combinations of format and
invocation state on every run. If a change makes the CLI prompt in CI or emit
prose into a JSON payload, that suite fails.

Many of the other tests exist because they caught something, and those name
the defect they guard against in their comment, so a change that reintroduces
it fails with an explanation rather than a diff.

The suite is checked by mutation: the source is deliberately broken in a few
dozen places and the suite has to notice. Three rounds of that have found
tests that could not fail — a keyring test whose fake ignored the delay it set
up, a truth table with no positive case that an all-`false` implementation
satisfied, a timing assertion that no real machine could ever trip.

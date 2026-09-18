---
name: traffic-check-in-ci
description: Adds a traffic or quality threshold to a pipeline using the Statable CLI, so a deploy fails when visitors collapse or bounce rate climbs past an agreed line. Use when someone wants analytics in CI, a guard after deploys, an alert on traffic loss, or a check that a release did not break tracking.
license: MIT
metadata:
  author: Key Arg B.V.
  product: Statable CLI
---

# Traffic check in CI

Turn a number into an exit code the pipeline can branch on.

## Steps

1. Confirm the CLI is available in the runner. Install options are in the repository README: a release binary, Homebrew, Scoop, or the Docker image `docker.io/statable/statable`.
2. Create a read-only key for the pipeline with `statable keys create ci --scope read` and pass it as `STATABLE_API_KEY`. Never put the key in the workflow file.
3. Pick the threshold with the person. Two shapes cover most cases:
   - `statable check --metric visitors --range 7d --min 100`
   - `statable check --metric bounce_rate --max 60`
4. Put the command in the pipeline step. A failed check exits non-zero, so the step fails without extra scripting.
5. For anything the check command does not cover, use `statable query --metric visitors,pageviews --range 7d` and branch on the JSON output.

## Rules

- Set the threshold from the site history, not from a round number. Pull the last weeks with `statable series --by day --range 30d` before agreeing on a line.
- A window that includes today is incomplete, so a daily check compares yesterday, not the running day.
- If the check fails right after a release, first confirm the tracking script still loads. A drop to zero is usually a broken install, not lost audience.

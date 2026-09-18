---
name: analytics-from-the-terminal
description: Pulls website numbers from Statable with the statable command and shapes them into a report or a JSON feed: visitors now, a period against the one before it, daily series, top pages, sources and countries, goals and funnels. Use when someone wants traffic figures in a terminal, a script, a cron job, or piped into jq for another tool.
license: MIT
metadata:
  author: Key Arg B.V.
  product: Statable CLI
---

# Analytics from the terminal

Get the numbers without opening a dashboard.

## Steps

1. Authenticate once with `statable auth login`. On a machine without a terminal prompt the command prints instructions instead of hanging.
2. `statable sites` lists what the key can read. `statable sites use example.com` fixes a default so later commands stay short.
3. Pick the command that answers the question:
   - `statable now` for visitors active right now
   - `statable stats --range 30d --compare previous` for a period against the one before it
   - `statable series --by day --range 30d` for a daily line
   - `statable top pages`, `statable top countries --range month` for breakdowns
   - `statable goals`, `statable funnels`, `statable funnel 45 --range 7d` for conversions
4. For anything custom, go through `statable query`, for example `statable query --metric visitors --dimension time:day --filter country=US`.
5. To reach an endpoint the commands do not wrap, use `statable api call GET /sites`, and find the shape with `statable api search` and `statable api describe /query`.

## Rules

- Output is JSON when piped, so `jq` works directly. Do not parse the human table.
- Numbers come back in the site timezone.
- Reading needs a key with read scope. Changing sites, goals or funnels needs write scope, and the CLI will say which one is missing.
- Statable stores no persistent visitor identifier, so there is no per-person export. Do not build one.

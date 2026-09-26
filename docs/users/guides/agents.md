# Using thinkingface from AI agents

An AI coding agent — Claude Code, or any client that speaks the Model Context Protocol — can run
the experiment loop you would otherwise run by hand: launch a training job, wait for it, read the
metrics, compare it with earlier runs, and write down what it learned. thinkingface gives an agent
three ways in, all backed by the same API and the same access tokens:

| Surface | Best for |
|---|---|
| The MCP server, `tf mcp` | Agents with MCP support. The experiment tools appear as native tools, with their arguments and limits described to the model. |
| The `tf` CLI with `--json` | Agents that work through a shell, and scripts. Every command prints machine-readable JSON on stdout. |
| The HTTP API, described by `/api/openapi.json` | Anything else — your own tooling, other languages. |

This page covers each of them, a typical loop, and how to keep an agent's token to the minimum it
needs. The runs themselves are logged by your training script as usual — see
[Tracking Experiments](experiments.md).

## Give the agent its own token

Hand an agent a token of its own rather than yours: a **write token restricted to the experiment
repository it works on, with an expiry**. It can then log runs, annotate them, set metric goals and
edit the project's notes in that one repository — and nothing else: it cannot push to another
repository, delete or rename anything, or mint itself a new token. Reads are not narrowed by the
restriction, so the agent can still read every repository it could read anyway.

Create it at **Settings → Access tokens**: scope **write**, an expiration of 7 or 30 days, and the
repository under **Restrict to repositories** (for example `datasets/alice/trackio-metrics`). Or,
from the server's command line:

```bash
docker compose exec -T api thinkingface admin token create alice \
    --name claude-ocr --expires-in-days 7 --repo datasets/alice/trackio-metrics > agent-token.txt
```

The repository must exist before a token can be restricted to it — a restricted token cannot create
repositories. Every request the token makes carries its id and name in the server's access log, so
what the agent did is attributable. See
[Restricting a token to repositories](../reference/authentication.md#restricting-a-token-to-repositories).

## The MCP server

`tf mcp` serves the `tf experiments` functionality over MCP's stdio transport. The agent's client
starts it as a subprocess and talks to it on stdin/stdout — there is no port to open and no daemon
to keep running. Install `tf` first ([tf CLI](../reference/tf-cli.md#installation)).

### Register it

With Claude Code, on a machine where `tf login` has already been run:

```bash
claude mcp add thinkingface -- tf mcp
```

To give it the agent's own token instead of your saved login, pass the endpoint and token as
environment variables:

```bash
claude mcp add thinkingface \
  -e THINKINGFACE_ENDPOINT=http://localhost:8080 \
  -e THINKINGFACE_API_KEY=tf_xxxxxxxxxxxx \
  -- tf mcp
```

Clients that take a JSON configuration — a project's `.mcp.json`, or any client with an
`mcpServers` map — use the same command and variables:

```json
{
  "mcpServers": {
    "thinkingface": {
      "command": "tf",
      "args": ["mcp"],
      "env": {
        "THINKINGFACE_ENDPOINT": "http://localhost:8080",
        "THINKINGFACE_API_KEY": "tf_xxxxxxxxxxxx"
      }
    }
  }
}
```

`tf mcp` resolves credentials exactly like every other `tf` command: the `--endpoint` / `--token`
flags, then `TF_ENDPOINT` / `THINKINGFACE_ENDPOINT` / `HF_ENDPOINT` and `THINKINGFACE_API_KEY` /
`TF_TOKEN` / `THINKINGFACE_TOKEN`, then the login `tf login` saved (see the
[credential resolution order](../reference/tf-cli.md#credential-resolution-order)). They are
resolved on the first tool call, not at startup, so a server started before you logged in keeps
running and answers each call with an error that says what to do until credentials exist.

`tf mcp` writes nothing to stderr unless started with `--verbose`, which logs credential resolution
and each request — useful when a client reports the server as failing.

### Tools

`repo` is always the experiment repository as `ns/name`. Each tool answers with the API's JSON.

| Tool | Arguments (`?` = optional) | Does |
|---|---|---|
| `list_experiment_repos` | `author?`, `search?` | Lists the repositories that hold experiments |
| `list_projects` | `repo` | A repository's projects, with run counts and metric goals |
| `list_runs` | `repo`, `project`, `group?`, `status?[]`, `tag?[]`, `archived?`, `sort?`, `order?`, `limit?` | Runs, filtered and sorted on the server — `sort` takes the same specs as `tf experiments runs --sort`, including `best:<metric>` |
| `get_run` | `repo`, `project`, `run` | One run: status, step, config, last / min / max of every metric |
| `get_metrics` | `repo`, `project`, `runs[]`, `keys?[]`, `x?` (`step` or `time`), `max_points?` | Metric series as `[x, y]` pairs, downsampled to `max_points` (**200 by default** to keep answers small, at most 5000) |
| `wait_for_run` | `repo`, `project`, `run`, `until?`, `timeout_seconds?`, `ignore_stale?` | Waits like `tf experiments wait` (`until` defaults to `status!=running`; `timeout_seconds` defaults to 600, at most 3600) |
| `config_diff` | `repo`, `project`, `runs?[]`, `include_meta?` | The config keys that differ between runs |
| `get_notes` | `repo`, `project` | The project's `NOTES.md`, with its `blob_sha` |
| `update_notes` | `repo`, `project`, `content`, `base_sha?`, `message?` | Replaces the notes |
| `annotate_run` | `repo`, `project`, `run`, `note?`, `tags?[]`, `archived?` | Changes only the fields given; `tags` replaces the list |
| `set_metric_goals` | `repo`, `project`, `goals` (`{metric: "min" \| "max" \| ""}`) | Merges goals; `""` removes one |

A few behaviours worth knowing when you read an agent's transcript:

- **A `wait_for_run` timeout is a normal answer**, `met: false` with `reason: "timeout"`: the agent
  calls again to keep waiting. `reason: "stopped"` means the run finished, failed or went stale
  without meeting the condition, so waiting longer would not help. While one call waits, the others
  keep working.
- **Mistakes come back as tool results, not protocol errors** — a missing argument, an `until` that
  does not parse, a 403 from the server, a notes conflict — each with a sentence the model can act
  on, so the agent sees it and corrects itself.
- **`update_notes` with the `blob_sha` from `get_notes` as `base_sha`** turns a concurrent edit (a
  person editing the notes in the web UI at the same time) into a conflict the agent has to resolve,
  instead of a lost write. Without `base_sha` it overwrites.
- The read-only tools are marked as such (`readOnlyHint`), so a client can let them run without
  asking you each time.

## A typical loop

The loop below is what an agent usually does with these tools; the same steps work from a shell
with the CLI.

1. **Declare what "better" means** once per project, so "best" is well defined:
   `set_metric_goals` with `{"val/CER": "min"}`, or `tf experiments goals alice/trackio-metrics ocr val/CER=min`.
2. **Launch the training job** with the trackio shim and a run name the agent chooses, for example
   `THINKINGFACE_REPO=alice/trackio-metrics python train.py --lr 3e-4 --run-name lr-3e-4`.
3. **Wait for it** — right away; the run does not have to exist yet: `wait_for_run` with
   `until: "step >= 12000 or status != running"`, or on the command line

    ```bash
    tf experiments wait alice/trackio-metrics ocr lr-3e-4 \
        --until 'step >= 12000 or status != running' --timeout 6h --json
    ```

    A job that crashes goes `stale` about two minutes after its last heartbeat, which ends the
    wait with `reason: "stopped"` instead of letting it run into the timeout.
4. **Read the result in context**: `list_runs` with `sort: "best:val/CER"` and a small `limit` for
   the leaderboard, `get_run` for the new run's last / min / max, `config_diff` to see what differs
   from the best run so far, `get_metrics` when the curve's shape matters.
5. **Write down what it learned**: `annotate_run` with a short note and a tag, and `update_notes` to
   add a line to the project notebook. A link written as `[lr 3e-4](run:lr-3e-4)` becomes a link to
   that run in the web UI, so the people reading the notes can click through to the curve.

The project notes live in the repository as `{project}/NOTES.md`, so the agent's findings are
versioned next to the metrics, visible on the project page, and reviewable like any other commit.

## From a shell: `--json` everywhere

Every `tf` command except `tf mcp` itself — `login`, `logout`, `status`, `whoami`, `up`,
`version` and all of `tf experiments` — takes `--json` and then prints one JSON object on stdout. Progress, warnings and errors stay on
stderr, and the exit codes are unchanged (`0` success, `1` failure, `2` usage error), so a script
or an agent can parse stdout without filtering it:

```bash
best=$(tf experiments runs alice/trackio-metrics ocr --sort best:val/CER --limit 1 --json |
       jq -r '.runs[0].name')
tf experiments diff alice/trackio-metrics ocr "$best" lr-3e-4 --json
```

`tf experiments wait` adds its own exit codes (`0` the condition holds, `1` timeout or the run
stopped), which makes it the natural thing to put between "start training" and "act on the result"
in a script. See the [tf CLI reference](../reference/tf-cli.md#tf-experiments) for every command.

## The HTTP API and its OpenAPI document

`GET /api/openapi.json` serves an OpenAPI 3.1 description of the programmatic surface: sign-in,
access tokens, the current user, server info, listing and creating repositories, and the whole
experiment API under `/api/v1/experiments`. Its `servers` entry is the instance's own `TF_PUBLIC_URL`,
so a client generated from it needs no editing:

```bash
curl -s http://localhost:8080/api/openapi.json | jq '.paths | keys'
```

The HuggingFace-compatible endpoints (apart from `whoami-v2` and repository creation) and the
git / Git LFS transports are deliberately left out — `huggingface_hub` and `git` define those. Authenticate with `Authorization: Bearer tf_...`, exactly
as for any other API call. On an instance running with `TF_REQUIRE_AUTH_FOR_READ`, fetching the
document itself needs a token too.

## Least privilege, in short

- Give each agent its **own write token, restricted to the repository it works on and expiring** —
  never your personal unrestricted token.
- **Create the repository beforehand**; a restricted token cannot.
- Let the client run the **read-only tools** without confirmation, and keep an eye on the ones that
  write (`annotate_run`, `update_notes`, `set_metric_goals`).
- Remember that **reads are instance-wide**: a token restricts what an agent can change, not what it
  can see. Keep data an agent must not read on an instance it has no token for.
- Revoke the token at **Settings → Access tokens** when the work is done, or let it expire.

## Related pages

- [Tracking Experiments](experiments.md) — logging runs, goals, notes, and the `tf experiments` commands
- [tf CLI](../reference/tf-cli.md#tf-mcp) — `tf mcp` and every other command
- [Authentication](../reference/authentication.md#restricting-a-token-to-repositories) — restricted tokens

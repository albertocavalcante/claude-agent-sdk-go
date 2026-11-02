"""Scrub raw recorded CLI transcripts into claude/testdata/transcripts.

Usage: python3 -I scripts/scrub-fixtures/scrub.py <raw-dir> [dest-dir]
       python3 -I scripts/scrub-fixtures/scrub.py --rescrub <dir>

--rescrub re-applies only the structural normalisation in place (ids and paths
are left alone), for committed transcripts after the rules grew.

<raw-dir> holds <group>/*.transcript.jsonl (core, m0, hostile-repo), as
written by `go run ./scripts/record-fixtures`: one {"t","dir","msg"} object per
line. Output goes to <dest-dir>/<group>/ (default claude/testdata/transcripts).

What it does, generically:
  * account{} values and anything email-, organization- or *uuid-like under it
    become "<redacted>";
  * every UUID becomes a deterministic placeholder (equal ids stay equal);
  * per-run temp paths become /work/...; /home/<user> style paths become
    /home/user;
  * skill, command, plugin and memory catalogues become short generic lists;
  * rate_limit_event.rate_limit_info keeps status and rateLimitType; usage
    numbers, reset times and overage details become fixed placeholders;
  * e-mail addresses, sk-ant-... tokens and "Bearer ..." strings are masked.

Machine- or person-specific substitutions (names of your own connectors,
paths of your own tools) do NOT live in this repository. Put them in a JSON
file outside the tree and point $SCRUB_MAP at it:

    {"replacements": [["Acme Mail Connector", "claude.ai Example Mail"],
                      ["/opt/acme/tools/server.py", "/work/example-mcp/server.py"]]}

Applied in order, as plain substring replacements, before everything else.

Afterwards run `go test ./claude -run Privacy`: that gate, not this script, is
the authority, and it must never be loosened to make a recording pass.
"""
import glob
import json
import os
import re
import sys

RESCRUB = len(sys.argv) > 1 and sys.argv[1] == "--rescrub"
if RESCRUB:
    sys.argv.pop(1)
SRC = sys.argv[1]
DST = sys.argv[2] if len(sys.argv) > 2 else "claude/testdata/transcripts"

MAP = []
if os.environ.get("SCRUB_MAP"):
    with open(os.environ["SCRUB_MAP"]) as fh:
        MAP = [tuple(x) for x in json.load(fh)["replacements"]]

UUID = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}")
SECRETS = [(re.compile(r"sk-ant-[A-Za-z0-9_-]+"), "<token>"), (re.compile(r"Bearer\s+[A-Za-z0-9._~+/=-]+"), "Bearer <token>")]
PATHS = [
    (re.compile(r"/private/tmp/claude-\d+/-private-var-folders-[A-Za-z0-9_-]+-T-record-\d+-work"), "/work/tmp"),
    (re.compile(r"-private-var-folders-[A-Za-z0-9_-]+-T-record-\d+-work"), "-work"),
    (re.compile(r"/(?:private/)?var/folders/[^/\"\\]+/[^/\"\\]+/T/"), "/work/tmp/"),
    (re.compile(r"/Users/[^/\"\\]+"), "/home/user"),
    (re.compile(r"/home/(?!user\b)[^/\"\\]+"), "/home/user"),
]

uuids = {}


def fake_uuid(m):
    s = m.group(0).lower()
    if s not in uuids:
        uuids[s] = "00000000-0000-4000-8000-%012d" % (len(uuids) + 1)
    return uuids[s]


def redact_account(v):
    if isinstance(v, dict):
        return {k: "<redacted>" for k in v}
    return "<redacted>"


def rate_limit(v):
    if not isinstance(v, dict):
        return v
    keep = {"status", "rateLimitType"}
    out = {}
    for k, x in v.items():
        if k in keep:
            out[k] = x
        elif k == "unifiedWindows":
            out[k] = {}
        elif isinstance(x, bool):
            out[k] = False
        elif isinstance(x, (int, float)):
            out[k] = 0
        elif isinstance(x, str):
            out[k] = "<redacted>"
        else:
            out[k] = None
    return out


def fix(o):
    if isinstance(o, dict):
        out = {}
        for k, v in o.items():
            if k == "rate_limit_info":
                v = rate_limit(v)
            elif k == "account":
                v = redact_account(v)
            elif k in ("slash_commands", "skills") and isinstance(v, list):
                v = ["compact", "context"] if k == "slash_commands" else ["example-skill"]
            elif k == "slashCommands" and isinstance(v, list):
                v = []
            elif k == "skills" and isinstance(v, dict):
                v = {}
            elif k == "commands" and isinstance(v, list):
                v = [{"name": "compact", "description": "<desc>", "argumentHint": ""}]
            elif k == "plugins" and isinstance(v, list):
                v = [{"name": "example-plugin", "path": "builtin", "source": "example-plugin@builtin"}]
            elif k == "memoryFiles" and isinstance(v, list):
                v = []
            elif k == "mcpInstructions":
                v = "<redacted>"
            elif k == "user_output_styles_dir":
                v = "/home/user/.claude/output-styles"
            elif k == "agents" and isinstance(v, list) and v and isinstance(v[0], dict):
                v = [dict(a, description="<desc>") for a in v]
            out[k] = fix(v)
        return out
    if isinstance(o, list):
        return [fix(x) for x in o]
    return o


def scrub_line(line):
    if RESCRUB:
        return json.dumps(fix(json.loads(line)), ensure_ascii=False, separators=(",", ":"))
    for a, b in MAP:
        line = line.replace(a, b)
    for rx, rep in PATHS:
        line = rx.sub(rep, line)
    line = UUID.sub(fake_uuid, line)
    rec = fix(json.loads(line))
    line = json.dumps(rec, ensure_ascii=False, separators=(",", ":"))
    for rx, rep in SECRETS:
        line = rx.sub(rep, line)
    return EMAIL.sub("<email>", line)


n = 0
if RESCRUB:
    for path in sorted(glob.glob(os.path.join(SRC, "*", "*.transcript.jsonl"))):
        with open(path) as fh:
            out = [scrub_line(l.rstrip("\n")) for l in fh if l.strip()]
        with open(path, "w") as fh:
            fh.write("\n".join(out) + "\n")
        n += 1
    print("rescrubbed", n)
    sys.exit(0)
for path in sorted(glob.glob(os.path.join(SRC, "*", "*.transcript.jsonl"))):
    group = os.path.basename(os.path.dirname(path))
    os.makedirs(os.path.join(DST, group), exist_ok=True)
    with open(path) as fh:
        out = [scrub_line(l.rstrip("\n")) for l in fh if l.strip()]
    with open(os.path.join(DST, group, os.path.basename(path)), "w") as fh:
        fh.write("\n".join(out) + "\n")
    n += 1
print("files", n, "uuids", len(uuids))

#!/usr/bin/env python3
"""Compare API session facts against the raw native transcripts (ground truth).
usage: audit_accuracy.py <api base> [sample size]
"""
import glob, json, os, random, sys, urllib.request, urllib.parse

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8787"
N = int(sys.argv[2]) if len(sys.argv) > 2 else 6
SOURCE = sys.argv[3] if len(sys.argv) > 3 else ""
HOME = os.path.expanduser("~")

def api(path):
    return json.load(urllib.request.urlopen(BASE + path))

# Blocks a harness writes under the user role that no user typed. This is the
# same closed list as canonical.InjectedMessagePrefixes, and
# TestAuditScriptMirrorsInjectedPrefixes keeps the two from drifting apart.
#
# It is a closed list on purpose. Treating any text that opens with a tag as
# injected would silently excuse a real divergence: <WORKFLOW> ... is a message
# a user typed, and counting it as harness output would make Flatline look
# right when it was wrong. Unknown tags are reported separately, as a warning,
# so a new harness block is noticed instead of being absorbed.
INJECTED = ("<local-command-caveat>", "<command-name>", "<command-message>", "<command-args>",
            "<local-command-stdout>", "<local-command-stderr>", "<local-command-result>",
            "<system-reminder>", "<task-notification>", "<subagent_notification>",
            "<environment_context>", "<user_instructions>", "<recommended_plugins>", "<turn_aborted>",
            "<fork-boilerplate>", "<teammate-message",
            "<bash-input>", "<bash-stdout>", "<bash-stderr>",
            "# AGENTS.md instructions", "Async agent launched successfully",
            "(Bash completed", "File does not exist")

# Angle-tags seen under the user role that the closed list does not name. They
# are counted as user turns (that is what Flatline does), and reported at the
# end so a human can decide whether a new harness block needs adding.
UNKNOWN_TAGS = {}

def injected(text):
    t = text.lstrip()
    for p in INJECTED:
        if t.startswith(p): return p
    if t.startswith("<"):
        end = min((i for i in (t.find(c) for c in " >\n") if i > 0), default=-1)
        if end > 1: UNKNOWN_TAGS[t[:end]] = UNKNOWN_TAGS.get(t[:end], 0) + 1
    return None

def raw_claude(sid):
    # A subagent writes its own transcript under the parent's directory; it is
    # its own session, so it is looked up by the agent id in its file name.
    files = (glob.glob(f"{HOME}/.claude/projects/*/{sid}.jsonl")
             or glob.glob(f"{HOME}/.claude/projects/*/*/subagents/agent-{sid}.jsonl"))
    if not files: return None
    own_thread = "/subagents/" in files[0]
    users = tools = results = 0; usage = {}; models = set()
    for line in open(files[0], encoding="utf-8", errors="replace"):
        try: d = json.loads(line)
        except Exception: continue
        if d.get("isSidechain") and not own_thread: continue
        m = d.get("message") or {}
        content = m.get("content")
        if isinstance(content, list):
            for b in content:
                t = b.get("type")
                if t == "tool_use": tools += 1
                elif t == "tool_result": results += 1
                elif t == "text" and m.get("role") == "user" and b.get("text", "").strip() and not injected(b["text"]): users += 1
        elif isinstance(content, str) and m.get("role") == "user" and content.strip() and not injected(content):
            users += 1
        if m.get("usage") and m.get("id"): usage[m["id"]] = m["usage"]
        if m.get("model"): models.add(m["model"])
    out_tokens = sum(u.get("output_tokens", 0) for u in usage.values()) if usage else None   # no usage record = unrecorded, not 0
    in_tokens = sum(u.get("input_tokens", 0) + u.get("cache_read_input_tokens", 0) + u.get("cache_creation_input_tokens", 0) for u in usage.values())
    return {"user_messages": users, "tool_calls": tools, "tool_results": results, "output_tokens": out_tokens, "input_tokens": in_tokens, "models": sorted(models)}

def raw_codex(sid):
    files = glob.glob(f"{HOME}/.codex/sessions/*/*/*/*{sid}*.jsonl")
    if not files: return None
    users = tools = results = aborted = 0; last_total = None; models = set(); injected_tags = set()
    for line in open(files[0], encoding="utf-8", errors="replace"):
        try: d = json.loads(line)
        except Exception: continue
        p = d.get("payload") or {}
        t = d.get("type")
        if t == "response_item":
            pt = p.get("type")
            if pt in ("function_call", "custom_tool_call"): tools += 1
            elif pt in ("function_call_output", "custom_tool_call_output"): results += 1
            elif pt == "message" and p.get("role") == "user":
                c = p.get("content"); txt = "".join(x.get("text", "") for x in c if isinstance(x, dict)) if isinstance(c, list) else str(c or "")
                tag = injected(txt) if txt.strip() else "<empty>"
                if tag: injected_tags.add(tag)   # harness-injected context, not a user turn
                else: users += 1
        elif t == "event_msg":
            if p.get("type") == "token_count" and p.get("info"): last_total = p["info"].get("total_token_usage")
            if p.get("type") == "turn_aborted": aborted += 1
        elif t == "turn_context" and p.get("model"): models.add(p["model"])
    return {"user_messages": users, "tool_calls": tools, "tool_results": results, "turn_aborted": aborted,
            "total_tokens": (last_total or {}).get("total_tokens"), "output_tokens": (last_total or {}).get("output_tokens"), "models": sorted(models), "injected": sorted(injected_tags)}

sessions = api("/api/v1/sessions?thread=all&empty=all&limit=200&sort=recent" + (f"&harness={SOURCE}" if SOURCE else ""))["sessions"]
random.seed(7)
sample = random.sample([s for s in sessions if s["source"] in ("claude_code", "codex")], min(N, len(sessions)))
mismatch = 0
for s in sample:
    detail = api("/api/v1/sessions/" + urllib.parse.quote(s["id"], safe="") + "?events=page&limit=1")["session"]
    raw = raw_claude(s["source_session_id"]) if s["source"] == "claude_code" else raw_codex(s["source_session_id"])
    if raw is None:
        print(f"[skip] {s['id'][:40]} raw file not found"); continue
    checks = [("user_message_count", raw["user_messages"]), ("tool_call_count", raw["tool_calls"]), ("tool_result_count", raw["tool_results"])]
    usage = detail.get("usage") or {}
    if "output_tokens" in raw and raw["output_tokens"] is not None: checks.append(("usage.output_tokens", raw["output_tokens"]))
    row = []
    for key, expected in checks:
        got = usage.get(key.split(".")[1]) if key.startswith("usage.") else detail.get(key)
        ok = got == expected
        mismatch += 0 if ok else 1
        row.append(f"{key}={got}{'' if ok else f'≠raw {expected}'}")
    extra = (f" turn_aborted(raw)={raw['turn_aborted']}" if "turn_aborted" in raw else "") + (f" injected={raw['injected']}" if raw.get("injected") else "")
    print(f"[{'ok' if all('≠' not in r for r in row) else 'DIFF'}] {s['source']:11s} {s['id'][-12:]} | " + " ".join(row) + extra)
for tag, count in sorted(UNKNOWN_TAGS.items(), key=lambda kv: -kv[1]):
    print(f"[warn] user-role text opens with {tag}, which the closed list does not name "
          f"({count} messages in this sample); counted as a user turn by both sides")
print(f"\nchecked {len(sample)} sessions, {mismatch} mismatching fields")

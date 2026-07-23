#!/usr/bin/env python3
"""Deterministic perf fixture set for the add-session-usage-summary gates.

Generates a sandbox HOME with .claude/projects, .codex/sessions, .gemini/tmp:
  - 30 Claude sessions x 60 messages (usage on every assistant line)
  - 1 long Claude session with 5,000 messages (tail-ingest gate)
  - 1 ~2x long session with 10,000 messages (informational slope point)
  - 10 Codex rollouts (token_count every 4 events + rate_limits)
  - 5 Gemini chats ($set replay with per-message tokens)
Content is seeded/deterministic: same bytes on every run.
"""
import json, os, sys

HOME = sys.argv[1]

def w(path, lines):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        for l in lines:
            f.write(l + "\n")

def claude_session(uuid, n):
    lines = []
    for i in range(n):
        ts = f"2026-07-01T{10 + i // 3600:02d}:{(i // 60) % 60:02d}:{i % 60:02d}Z"
        if i % 2 == 0:
            lines.append(json.dumps({
                "type": "user", "uuid": f"{uuid}-u{i}", "timestamp": ts, "sessionId": uuid,
                "message": {"role": "user", "content": f"user turn {i} in {uuid} lorem ipsum dolor sit amet"}}))
        else:
            lines.append(json.dumps({
                "type": "assistant", "uuid": f"{uuid}-a{i}", "timestamp": ts, "sessionId": uuid,
                "message": {"role": "assistant", "model": "model-bench",
                            "content": [{"type": "text", "text": f"assistant reply {i} " + "x" * 200}],
                            "usage": {"input_tokens": 100 + i, "output_tokens": 50 + i,
                                      "cache_read_input_tokens": 10, "cache_creation_input_tokens": 5}}}))
    return lines

proj = os.path.join(HOME, ".claude", "projects", "-Users-bench-proj")
for s in range(30):
    uuid = f"bench-{s:04d}-0000-4000-8000-{s:012d}"
    w(os.path.join(proj, uuid + ".jsonl"), claude_session(uuid, 60))

LONG = "long0000-0000-4000-8000-000000005000"
w(os.path.join(proj, LONG + ".jsonl"), claude_session(LONG, 5000))
LONG2 = "long0000-0000-4000-8000-000000010000"
w(os.path.join(proj, LONG2 + ".jsonl"), claude_session(LONG2, 10000))

codex_dir = os.path.join(HOME, ".codex", "sessions", "2026", "07", "01")
for s in range(10):
    uuid = f"0199bench-{s:04d}"  # not a real uuid shape; filename needs one:
    uuid = f"0199aaaa-bbbb-4ccc-8ddd-{s:012d}"
    lines = [json.dumps({"timestamp": "2026-07-01T10:00:00Z", "type": "session_meta",
                         "payload": {"id": uuid, "cwd": "/Users/bench/proj"}})]
    for i in range(40):
        ts = f"2026-07-01T10:{i//60:02d}:{i%60:02d}Z"
        lines.append(json.dumps({"timestamp": ts, "type": "response_item",
                                 "payload": {"type": "message", "role": "user" if i % 2 == 0 else "assistant",
                                             "content": [{"type": "input_text" if i % 2 == 0 else "output_text",
                                                          "text": f"turn {i} " + "y" * 100}]}}))
        if i % 4 == 3:
            lines.append(json.dumps({"timestamp": ts, "type": "event_msg", "payload": {
                "type": "token_count",
                "info": {"total_token_usage": {"input_tokens": 1000 * (i + 1), "cached_input_tokens": 300 * (i + 1),
                                               "output_tokens": 100 * (i + 1), "reasoning_output_tokens": 50 * (i + 1),
                                               "total_tokens": 1100 * (i + 1)}},
                "rate_limits": {"limit_id": "codex", "plan_type": "plus",
                                "primary": {"used_percent": 10.0 + i, "window_minutes": 10080, "resets_at": 1785000000}}}}))
    w(os.path.join(codex_dir, f"rollout-2026-07-01T10-00-{s:02d}-{uuid}.jsonl"), lines)

for s in range(5):
    sid = f"gem-bench-{s:04d}"
    msgs = []
    for i in range(50):
        ts = f"2026-07-01T10:{i//60:02d}:{i%60:02d}Z"
        if i % 2 == 0:
            msgs.append({"id": f"m{i}", "timestamp": ts, "type": "user",
                         "content": [{"text": f"user {i} " + "z" * 80}]})
        else:
            msgs.append({"id": f"m{i}", "timestamp": ts, "type": "gemini", "model": "gemini-bench",
                         "content": [{"text": f"reply {i} " + "z" * 150}],
                         "tokens": {"input": 500 + i, "output": 60 + i, "cached": 20,
                                    "thoughts": 30, "tool": 0, "total": 610 + 2 * i}})
    lines = [json.dumps({"sessionId": sid, "projectHash": f"h{s}", "startTime": "2026-07-01T10:00:00Z",
                         "lastUpdated": "2026-07-01T11:00:00Z", "kind": "chat"}),
             json.dumps({"$set": {"messages": msgs}})]
    w(os.path.join(HOME, ".gemini", "tmp", f"h{s}", "chats", f"session-2026-07-01T10-00-{s:04d}.jsonl"), lines)

print(f"fixtures written under {HOME}")

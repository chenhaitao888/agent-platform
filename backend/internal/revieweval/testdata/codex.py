import json, os, sys, time

args = sys.argv[1:]
if args == ["--version"]:
    print("codex-cli 0.160.0")
elif args == ["exec", "--help"]:
    print('''Options:
  -s, --sandbox <MODE>
          [possible values: read-only,workspace-write,danger-full-access]
      --ephemeral
      --output-schema <FILE>
  -o, --output-last-message <FILE>
      --json
      --ignore-user-config
      --ignore-rules
''')
elif args == ["--help"]:
    print('''Options:
  -c, --config <KEY=VALUE>
      --no-daemon
  -a, --ask-for-approval <POLICY>
  -m, --model <MODEL>
''')
else:
    request = json.load(sys.stdin)
    # Capture must use the real fixed Git diff, without labels in its envelope.
    assert "diff --git" in request["patch"]
    assert "issues" not in request
    assert not os.path.exists("suite.json")
    if MODE == "timeout-tenant" and os.path.exists("records.py"):
        time.sleep(30)
    report = dict(schemaVersion="1.0", baseSha=request["baseSha"], headSha=request["headSha"], findings=[])
    if os.path.exists("divider.py"):
        report["findings"].append(dict(title="Zero count regression", description="average(10, 0) now raises ZeroDivisionError.", severity="HIGH", confidence=0.95, path="divider.py", startLine=2, endLine=2))
    if MODE == "wrong-refs":
        report["headSha"] = "f" * 40
    if MODE == "nonzero":
        print("private-diagnostic", file=sys.stderr)
        sys.exit(23)
    output = args[args.index("--output-last-message")+1]
    with open(output, "w") as f:
        f.write("private-diagnostic" if MODE == "invalid-json" else json.dumps(report))

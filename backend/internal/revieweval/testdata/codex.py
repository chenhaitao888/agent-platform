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
    if MODE == "extended-mixed" and os.path.exists("documents/update.py"):
        time.sleep(30)
    if MODE == "extended-mixed" and os.path.exists("orders/repository.py"):
        print("private-diagnostic", file=sys.stderr)
        sys.exit(23)
    if MODE == "extended-mixed" and os.path.exists("permissions/policy.py"):
        sys.exit(0)  # Missing output must not become a successful empty review.
    report = dict(schemaVersion="1.0", baseSha=request["baseSha"], headSha=request["headSha"], findings=[])
    if os.path.exists("divider.py") and MODE != "extended-mixed":
        report["findings"].append(dict(title="Zero count regression", description="average(10, 0) now raises ZeroDivisionError.", severity="HIGH", confidence=0.95, path="divider.py", startLine=2, endLine=2))
    if MODE == "wrong-refs" or (MODE == "extended-mixed" and os.path.exists("billing/rates.py")):
        report["headSha"] = "f" * 40
    if MODE == "nonzero":
        print("private-diagnostic", file=sys.stderr)
        sys.exit(23)
    output = args[args.index("--output-last-message")+1]
    with open(output, "w") as f:
        malformed = MODE == "invalid-json" or (MODE == "extended-mixed" and os.path.exists("accounts/service.py"))
        f.write("private-diagnostic" if malformed else json.dumps(report))

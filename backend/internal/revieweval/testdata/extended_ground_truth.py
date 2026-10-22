"""Behavioral oracle for reviewed embedded examples, never sent to the model."""
import importlib
import json
from pathlib import Path
import sys
import tempfile

case = json.load(sys.stdin)
sys.dont_write_bytecode = True
with tempfile.TemporaryDirectory() as directory:
    for name, source in case["files"].items():
        target = Path(directory) / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(source)
    sys.path.insert(0, directory)
    name = case["caseId"]
    if name == "cross-file-rate-unit":
        result = importlib.import_module("billing.invoice").invoice_total(100)
    elif name == "cross-file-return-contract":
        view = importlib.import_module("orders.view").order_view
        try:
            result = view({42: {"id": 42, "total": 100}}, 42)
        except KeyError:
            result = "KeyError"
    elif name == "tenant-write-guard":
        update = importlib.import_module("documents.update").update_title
        records = {7: {"tenant_id": "tenant-b", "title": "original"}}
        allowed = update(records, "tenant-a", 7, "changed")
        result = {"allowed": allowed, "title": records[7]["title"]}
        assert update(records, "tenant-b", 7, "owned")
        assert records[7]["title"] == "owned"
    elif name == "tenant-cache-key":
        service = importlib.import_module("accounts.service").AccountService()
        rows = [{"id": 7, "tenant_id": "tenant-a", "balance": 10},
                {"id": 7, "tenant_id": "tenant-b", "balance": 90}]
        assert service.get(rows, "tenant-a", 7)["balance"] == 10
        result = service.get(rows, "tenant-b", 7)["tenant_id"]
    elif name == "safe-rate-migration":
        total = importlib.import_module("checkout.invoice").invoice_total
        result = [total(value) for value in [0, 10, 100, -10]]
    elif name == "policy-timeout-fail-open":
        delete = importlib.import_module("permissions.delete").delete_resource
        class Policy:
            def __init__(self, answer):
                self.answer = answer
            def allows(self, tenant, resource):
                assert tenant == "tenant-a" and resource == 7
                if self.answer is None:
                    raise TimeoutError("policy unavailable")
                return self.answer
        for answer in [False, True]:
            records = {7: "owned"}
            assert delete(Policy(answer), records, "tenant-a", 7) == answer
            assert (7 in records) != answer
        records = {7: "protected"}
        result = {"allowed": delete(Policy(None), records, "tenant-a", 7),
                  "present": 7 in records}
    else:
        raise AssertionError("unknown oracle")
print(json.dumps(result, sort_keys=True))

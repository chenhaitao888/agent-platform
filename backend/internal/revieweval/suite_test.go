package revieweval_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"agent-platform/backend/internal/revieweval"
)

// Check the observable regression behind each ground truth label. These are
// tiny trusted Python functions, evaluated outside the review agent itself.
func TestSyntheticGroundTruthHasReproducibleRegressions(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range revieweval.DefaultSuite().Cases {
		t.Run(c.ID, func(t *testing.T) {
			data, _ := json.Marshal(c)
			command := exec.Command(python, "-c", `import json,sys
c=json.load(sys.stdin)
def observe(files):
    namespace={}
    for name,source in files.items():
        if name.endswith('.py'): exec(source,namespace)
    try:
        if 'average' in namespace: return namespace['average'](10,0)
        if 'find_record' in namespace: return namespace['find_record']([dict(id=7,tenant_id='other')],'mine',7)
        if 'item_at' in namespace: return namespace['item_at']([],0)
        return namespace['label']('Ada')
    except (ZeroDivisionError,IndexError) as error: return type(error).__name__
base,head=observe(c['base']),observe(c['head'])
assert (base!=head)==bool(c['issues']), (c['id'],base,head)
for issue in c['issues']:
    assert 1<=issue['startLine']<=issue['endLine']<=len(c['head'][issue['path']].splitlines())
`)
			command.Stdin = strings.NewReader(string(data))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("ground truth: %v %s", err, output)
			}
		})
	}
}

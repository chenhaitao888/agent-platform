package apiapp_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type deploymentFixture struct {
	root, head, base, ca, credential, docker string
	gitlab, model                            *httptest.Server
	directory                                string
	modelCalls                               *atomic.Int32
}

func gitFixture(t *testing.T, directory string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %s: %v %s", args[0], err, output)
	}
	return strings.TrimSpace(string(output))
}

func newDeploymentFixture(t *testing.T) deploymentFixture {
	t.Helper()
	cleanDeployment(t)
	fixture := deploymentFixture{root: filepath.Join(t.TempDir(), "workspaces"), directory: t.TempDir(), modelCalls: new(atomic.Int32)}
	source := t.TempDir()
	gitFixture(t, source, "init", "--initial-branch=master")
	gitFixture(t, source, "config", "user.name", "API Assembly Test")
	gitFixture(t, source, "config", "user.email", "api-assembly@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("shared base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, source, "add", "README.md")
	gitFixture(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "base")
	fixture.base = gitFixture(t, source, "rev-parse", "HEAD")
	gitFixture(t, source, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(source, "feature.txt"), []byte("feature-only change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, source, "add", "feature.txt")
	gitFixture(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "feature")
	fixture.head = gitFixture(t, source, "rev-parse", "HEAD")
	repos := t.TempDir()
	gitFixture(t, source, "clone", "--bare", "--", source, filepath.Join(repos, "repo.git"))
	execPath := gitFixture(t, source, "--exec-path")
	backend := &cgi.Handler{Path: filepath.Join(execPath, "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + repos, "GIT_HTTP_EXPORT_ALL=1"}}
	fixture.gitlab = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repo.git/") {
			user, password, ok := r.BasicAuth()
			if !ok || user != "oauth2" || password != "api-gitlab-token" {
				w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			backend.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("PRIVATE-TOKEN") != "api-gitlab-token" {
			t.Error("GitLab API did not receive its service credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/master"):
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"id": fixture.base}})
		case strings.HasSuffix(r.URL.Path, "/merge_base"):
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fixture.base})
		case strings.Contains(r.URL.Path, "/commits/"):
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/api/v4/projects/42":
			_ = json.NewEncoder(w).Encode(map[string]string{"http_url_to_repo": fixture.gitlab.URL + "/repo.git"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.gitlab.Close)
	fixture.ca = filepath.Join(fixture.directory, "gitlab-ca.pem")
	if err := os.WriteFile(fixture.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.gitlab.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.model = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.RequestURI() != "/v1/responses" || r.Header.Get("Authorization") != "Bearer api-model-token" {
			t.Error("model endpoint or service identity changed")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fixture.modelCalls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if _, err := os.Stat(filepath.Join(fixture.directory, "model-hang")); err == nil {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(fixture.model.Close)
	fixture.credential = filepath.Join(fixture.directory, "model.json")
	if err := os.WriteFile(fixture.credential, []byte(`{"servicePrincipal":"api-review-service","token":"api-model-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.docker = filepath.Join(fixture.directory, "docker-fixture")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(fixture.directory)
	script := "#!" + python + "\n" + `import base64,json,os,sys
directory=` + string(quoted) + `
args=sys.argv[1:]
with open(directory+"/commands.jsonl","a") as capture: capture.write(json.dumps(args)+"\n")
if args[0]=="image":
    print(json.dumps([dict(Id="sha256:"+"a"*64,Os="linux",Architecture="arm64")]))
elif args[0]=="create":
    with open(directory+"/"+args[args.index("--name")+1],"w") as target: json.dump(args,target)
    print("b"*64)
elif args[0]=="start":
    with open(directory+"/"+args[-1]) as source: created=json.load(source)
    if "--probe" in created:
        print(json.dumps(dict(modelRelaySupported=not os.path.exists(directory+"/bad-worker"),gatewaySupported=True,sandboxVerified=True,profile=dict(version="0.160.0",binaryPath="/opt/codex/codex",binarySha256="c"*64,integration="exec",sandbox="read-only",ephemeral=True,requiredFlags=["--sandbox","--ephemeral","--output-schema","--output-last-message","--json","--ignore-user-config","--ignore-rules","--no-daemon","--ask-for-approval","--model","--config"]))))
    else:
        initial=json.loads(sys.stdin.readline())
        if "api-model-token" in json.dumps(initial): sys.exit(28)
        model=created[created.index("--model")+1]
        body=base64.b64encode(json.dumps(dict(model=model,input="fixture",stream=True)).encode()).decode()
        print(json.dumps(dict(type="request",request=dict(method="POST",path="/responses",body=body))),flush=True)
        while True:
            line=sys.stdin.readline()
            if not line: sys.exit(29)
            response=json.loads(line)
            if response["type"]=="end": break
        request=initial["input"]
        print(json.dumps(dict(type="result",result=dict(report=dict(schemaVersion="1.0",baseSha=request["baseSha"],headSha=request["headSha"],findings=[])))),flush=True)
elif args[0]=="rm":
    os.remove(directory+"/"+args[-1])
`
	if err := os.WriteFile(fixture.docker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture deploymentFixture) configure(t *testing.T, review bool) {
	t.Helper()
	for key, value := range map[string]string{"GITLAB_BASE_URL": fixture.gitlab.URL, "GITLAB_TOKEN": "api-gitlab-token", "WORKSPACE_ROOT": fixture.root, "GITLAB_CA_FILE": fixture.ca} {
		t.Setenv("AGENT_PLATFORM_"+key, value)
	}
	if review {
		for key, value := range map[string]string{"REVIEW_ENABLED": "true", "DOCKER_BINARY": fixture.docker, "DOCKER_HOST": "unix:///tmp/api-deployment-docker.sock", "REVIEW_WORKER_IMAGE": "sha256:" + strings.Repeat("a", 64), "REVIEW_MODEL": "deployment-review-model", "MODEL_BASE_URL": fixture.model.URL + "/v1", "MODEL_SERVICE_PRINCIPAL": "api-review-service", "MODEL_CREDENTIAL_FILE": fixture.credential, "MODEL_CA_FILE": fixture.ca} {
			t.Setenv("AGENT_PLATFORM_"+key, value)
		}
	}
}

func createAPITask(t *testing.T, handler http.Handler, fixture deploymentFixture) {
	t.Helper()
	body := `{"requestId":"create","idempotencyKey":"create","tenantId":"test","type":"PR_REVIEW","goal":"review","repository":{"provider":"gitlab","repositoryId":"42","headSha":"` + fixture.head + `"}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("API create: %d %s", response.Code, response.Body.String())
	}
}

// This executable replaces only the external model CLI in real Docker tests.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--sandbox-boundary" {
		checkBoundary()
		checkBlockedSyscalls()
		return
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("codex-cli 9.999.0")
		return
	}
	if len(args) == 1 && args[0] == "--help" {
		fmt.Print("Options:\n      --no-daemon\n  -a, --ask-for-approval <POLICY>\n  -m, --model <MODEL>\n")
		return
	}
	if len(args) == 2 && args[0] == "exec" && args[1] == "--help" {
		fmt.Print("Options:\n  -s, --sandbox <MODE>\n          [possible values: read-only, workspace-write]\n      --ephemeral\n      --output-schema <FILE>\n  -o, --output-last-message <FILE>\n      --json\n      --ignore-user-config\n      --ignore-rules\n")
		return
	}
	if len(args) == 1 && args[0] == "--escaped-child" {
		for {
			time.Sleep(time.Second)
		}
	}
	for _, arg := range args {
		if arg == "sandbox" {
			if native := nativeBinary(); native != "" {
				command := exec.Command(native, args...)
				command.Stdout = os.Stdout
				command.Stderr = os.Stderr
				if err := command.Run(); err != nil {
					os.Exit(1)
				}
				return
			}
			if err := exec.Command("/bin/true").Run(); err != nil {
				panic(err)
			}
			return
		}
	}
	var input struct {
		BaseSHA string `json:"baseSha"`
		HeadSHA string `json:"headSha"`
		Patch   string `json:"patch"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		panic(err)
	}
	var output string
	for i, arg := range args {
		if arg == "--output-last-message" && i+1 < len(args) {
			output = args[i+1]
		}
	}
	defer func() {
		if failure := recover(); failure != nil {
			report := map[string]any{"schemaVersion": "1.0", "baseSha": input.BaseSHA, "headSha": input.HeadSHA, "findings": []any{map[string]any{"title": "Fixture boundary failure", "description": fmt.Sprint(failure), "severity": "HIGH", "confidence": 1, "path": "feature.txt", "startLine": 1, "endLine": 1}}}
			content, _ := json.Marshal(report)
			_ = os.WriteFile(output, content, 0o600)
		}
	}()
	checkBoundary()
	if native := nativeBinary(); native != "" {
		checkBlockedSyscalls()
		command := exec.Command(native, "--no-daemon", "--ask-for-approval", "never", "sandbox", "-c", `sandbox_mode="read-only"`, "/opt/codex/codex", "--sandbox-boundary")
		if output, err := command.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("native sandbox boundary: %v %s", err, output))
		}
	}
	fixtureMode, _ := os.ReadFile("feature.txt")
	if strings.Contains(string(fixtureMode), "WORKER_FIXTURE_HANG") {
		child := exec.Command(os.Args[0], "--escaped-child")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			panic(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	report := map[string]any{"schemaVersion": "1.0", "baseSha": input.BaseSHA, "headSha": input.HeadSHA, "findings": []any{}}
	content, err := json.Marshal(report)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(output, content, 0o600); err != nil {
		panic(err)
	}
}

func nativeBinary() string {
	self, err := os.Stat(os.Args[0])
	if err != nil {
		return ""
	}
	native, err := os.Stat("/opt/codex/bin/codex")
	if err != nil || os.SameFile(self, native) {
		return ""
	}
	return "/opt/codex/bin/codex"
}

func checkBlockedSyscalls() {
	_, _, errno := syscall.RawSyscall(syscall.SYS_KEYCTL, 0, 0, 0)
	if errno != syscall.EPERM {
		panic("keyctl was not blocked")
	}
	if descriptor, err := syscall.Socket(38, syscall.SOCK_SEQPACKET, 0); err != syscall.EPERM {
		if descriptor >= 0 {
			_ = syscall.Close(descriptor)
		}
		panic("AF_ALG was not blocked")
	}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_UNSHARE, syscall.CLONE_NEWNS, 0, 0); errno != syscall.EPERM {
		panic("unshare without a fresh user namespace was not blocked")
	}
	if err := syscall.Mount("none", "/", "", syscall.MS_BIND|syscall.MS_REMOUNT, ""); err != syscall.EPERM {
		panic("read-write remount was not blocked")
	}
	status, _ := os.ReadFile("/proc/self/status")
	if !strings.Contains(string(status), "Seccomp:\t2") {
		panic("seccomp was not enforced")
	}
}

func checkBoundary() {
	if os.Geteuid() == 0 {
		panic("root worker")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		panic(err)
	}
	if !strings.Contains(string(status), "CapEff:\t0000000000000000") || !strings.Contains(string(status), "NoNewPrivs:\t1") {
		panic("capabilities")
	}
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "feature.txt"))
	if err != nil || !strings.Contains(string(data), "feature-only change") {
		panic("workspace input")
	}
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		panic(err)
	}
	for _, path := range []string{"/", filepath.Dir(cwd)} {
		found := false
		for _, line := range strings.Split(string(mounts), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 5 && fields[4] == path && strings.HasPrefix(fields[5], "ro") {
				found = true
			}
		}
		if !found {
			panic("writable mount")
		}
	}
	if os.WriteFile(filepath.Join(cwd, "worker-write"), []byte("unsafe"), 0o600) == nil {
		panic("writable workspace")
	}
	if os.WriteFile("/worker-write", []byte("unsafe"), 0o600) == nil {
		panic("writable root")
	}
	interfaces, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		panic(err)
	}
	for _, network := range strings.Split(string(interfaces), "\n") {
		if name, _, ok := strings.Cut(network, ":"); ok && strings.TrimSpace(name) != "lo" {
			flags, err := os.ReadFile("/sys/class/net/" + strings.TrimSpace(name) + "/flags")
			if err != nil {
				panic(err)
			}
			value, err := strconv.ParseUint(strings.TrimSpace(string(flags)), 0, 64)
			if err != nil || value&1 != 0 {
				panic("network access")
			}
		}
	}
	routes, err := os.ReadFile("/proc/net/route")
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(routes), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[1] == "00000000" && fields[7] == "00000000" {
			panic("IPv4 default route")
		}
	}
	routes, err = os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(routes), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 10 && fields[0] == strings.Repeat("0", 32) && fields[1] == "00" {
			flags, err := strconv.ParseUint(fields[8], 16, 32)
			if err != nil {
				panic(err)
			}
			// Linux retains synthetic unreachable routes on lo with RTF_REJECT
			// (0x200), even in a network=none namespace. Reject usable defaults.
			if flags&1 != 0 && flags&0x200 == 0 {
				panic("IPv6 default route")
			}
		}
	}
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "GITLAB_TOKEN", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT"} {
		if os.Getenv(key) != "" {
			panic("inherited credential or daemon endpoint")
		}
	}
	if _, err := os.Stat("/var/run/docker.sock"); !os.IsNotExist(err) {
		panic("mounted daemon socket")
	}
	for _, directory := range []string{os.Getenv("HOME"), os.Getenv("CODEX_HOME")} {
		if !strings.HasPrefix(directory, "/tmp/") {
			panic("persistent personal config")
		}
	}
}

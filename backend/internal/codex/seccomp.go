package codex

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
)

// Vendored from moby/profiles tag seccomp/v0.2.1, SHA-256
// 536529b665dd0972c37bfb569f5d4ac8a53592e7b00752bc39ff063ca9864c74.
// See policies/LICENSE.moby-profiles and policies/README.md for provenance.
//
//go:embed policies/moby-seccomp-v0.2.1.json
var dockerSeccompBaseline []byte

const (
	SeccompDockerDefault = "docker-default"
	SeccompCodexBwrap    = "codex-bwrap"
)

type seccompArgument struct {
	Index     uint   `json:"index"`
	Value     uint64 `json:"value"`
	ValueTwo  uint64 `json:"valueTwo,omitempty"`
	Operation string `json:"op"`
}
type seccompRule struct {
	Names     []string          `json:"names"`
	Action    string            `json:"action"`
	Arguments []seccompArgument `json:"args,omitempty"`
	Errno     uint              `json:"errnoRet,omitempty"`
	Comment   string            `json:"comment,omitempty"`
}

// Only these two reviewed policies are available. A task cannot provide a file,
// raw JSON, "unconfined", or arbitrary Docker security options.
func deploymentSeccomp(name string) (string, []byte, string, error) {
	switch name {
	case "", SeccompDockerDefault:
		return SeccompDockerDefault, nil, "", nil
	case SeccompCodexBwrap:
	default:
		return "", nil, "", ErrInvalidRunnerConfig
	}
	var profile map[string]any
	if err := json.Unmarshal(dockerSeccompBaseline, &profile); err != nil {
		return "", nil, "", ErrInvalidRunnerConfig
	}
	rules, ok := profile["syscalls"].([]any)
	if !ok || profile["defaultAction"] != "SCMP_ACT_ERRNO" {
		return "", nil, "", ErrInvalidRunnerConfig
	}
	// The platform supports 64-bit Linux/amd64 and Linux/arm64 images. Do not
	// enable their 32-bit compatibility ABIs and socketcall argument multiplexer.
	profile["archMap"] = []any{
		map[string]any{"architecture": "SCMP_ARCH_X86_64", "subArchitectures": []string{}},
		map[string]any{"architecture": "SCMP_ARCH_AARCH64", "subArchitectures": []string{}},
	}
	const (
		cloneNewUser = 0x10000000
		msReadonly   = 1
		msNosuid     = 2
		msNodev      = 4
		msNoexec     = 8
		msRemount    = 32
		msBind       = 4096
		msRecursive  = 16384
		msSilent     = 32768
		msPrivate    = 1 << 18
		msSlave      = 1 << 19
		msMagic      = 0xc0ed0000
	)
	argumentRule := func(syscall string, index uint, value, mask uint64, comment string) seccompRule {
		argument := seccompArgument{Index: index, Value: value, Operation: "SCMP_CMP_EQ"}
		if mask != 0 {
			argument.Value = mask
			argument.ValueTwo = value
			argument.Operation = "SCMP_CMP_MASKED_EQ"
		}
		return seccompRule{Names: []string{syscall}, Action: "SCMP_ACT_ALLOW", Arguments: []seccompArgument{argument}, Comment: comment}
	}
	// Kernel capabilities remain zero in the original container namespace.
	// bwrap gains setup capabilities only inside its new unprivileged user NS.
	for _, syscall := range []string{"clone", "unshare"} {
		rules = append(rules, argumentRule(syscall, 0, cloneNewUser, cloneNewUser, "Require a new user namespace"))
	}
	for _, mount := range []struct {
		flags   uint64
		purpose string
	}{
		{msSilent | msSlave | msRecursive, "Stop propagating mounts to the outer namespace"},
		{msNodev | msNosuid, "Temporary sandbox root"},
		{msNosuid | msNoexec | msNodev, "Sandbox proc filesystem"},
		{msNosuid | msNoexec, "Sandbox devpts filesystem"},
		{msSilent | msMagic | msBind | msRecursive, "Initial newroot bind"},
		{msSilent | msRecursive | msPrivate, "Isolate oldroot before detach"},
		{msSilent | msBind | msRecursive, "Recursive sandbox bind"},
		{msSilent | msBind, "Individual sandbox bind"},
	} {
		rules = append(rules, argumentRule("mount", 3, mount.flags, 0, mount.purpose))
	}
	rules = append(rules,
		argumentRule("mount", 3, msBind|msRemount|msReadonly, msBind|msRemount|msReadonly, "Only read-only bind remounts"),
		seccompRule{Names: []string{"pivot_root"}, Action: "SCMP_ACT_ALLOW", Comment: "Switch root within the new user/mount namespace"},
		argumentRule("umount2", 1, 2, 0, "Only lazy-detach oldroot (MNT_DETACH)"),
		seccompRule{Names: []string{"socketcall"}, Action: "SCMP_ACT_ERRNO", Errno: 1, Comment: "Deny the legacy socket argument multiplexer"},
	)
	profile["syscalls"] = rules
	content, err := json.Marshal(profile)
	if err != nil {
		return "", nil, "", ErrInvalidRunnerConfig
	}
	return SeccompCodexBwrap, content, fmt.Sprintf("%x", sha256.Sum256(content)), nil
}

# Review Worker seccomp policy

The `docker-default` deployment selection leaves the daemon's default policy in
place. `codex-bwrap` is a separate, explicitly selected policy for the isolated
read-only Review Worker. Task requests cannot select or edit it.

The baseline is vendored unchanged from
[moby/profiles, tag seccomp/v0.2.1](https://github.com/moby/profiles/blob/seccomp/v0.2.1/seccomp/default.json).
Its SHA-256 is `536529b665dd0972c37bfb569f5d4ac8a53592e7b00752bc39ff063ca9864c74`.
The Apache-2.0 license is included as `LICENSE.moby-profiles`. Runtime generation
in `../seccomp.go` appends reviewed rules and records the SHA-256 of the actual
bytes sent to Docker. It never downloads a policy at runtime.

Changes from that baseline:

- Enable only the 64-bit amd64/arm64 ABIs; omit 32-bit compatibility architectures.
- Deny `socketcall` explicitly. Baseline `socket` rules deny AF_ALG and AF_VSOCK.
- Add namespace-creating `clone`/`unshare` rules that require a new user namespace.
  Ordinary process/thread creation retains the baseline rules.
- Allow bubblewrap's exact mount flags for slave/private propagation, temporary
  root, proc, devpts, and bind mounts. Bind remounts must set MS_RDONLY; read-write
  remounts are not allowed.
- Allow `pivot_root` and `umount2(MNT_DETACH)` for namespace root setup.

The original container continues to have zero capabilities, a non-root UID,
no-new-privileges, no network, a read-only root and read-only task mount.
Kernel capability checks prevent mount/root changes in the original namespace;
bubblewrap obtains setup privileges only inside a new user namespace. Other
baseline restrictions, including keyrings, BPF and io_uring, remain enforced.
Seccomp checks syscall arguments, not the strings behind mount source/target
pointers; the kernel's namespace and capability checks remain essential.

This policy exposes nested user/mount namespaces that Docker normally blocks;
that increases reachable kernel code. It is intended only for this controlled
Worker profile and is not a general recommendation to replace Docker's default.
Keep Docker/Linux patched and verify the policy and native sandbox on each
deployment. Kernel/LSM restrictions can still reject initialization; startup must
fail in that case. The policy does not disable AppArmor/SELinux or change sysctls.

See [Docker's seccomp documentation](https://docs.docker.com/engine/security/seccomp/)
and [the official OpenAI sandbox documentation](https://learn.chatgpt.com/docs/sandboxing).

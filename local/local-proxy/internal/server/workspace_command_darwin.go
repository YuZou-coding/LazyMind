//go:build darwin

package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func runContainedWorkspaceCommand(ctx context.Context, root, cwd string, argv []string, env map[string]string) (workspaceCommandResult, error) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		return workspaceCommandResult{}, errWorkspaceContainmentUnavailable
	}
	profile := fmt.Sprintf(`(version 1)
(deny default)
(allow process*)
(allow sysctl-read)
(allow mach-lookup)
(allow file-read* (subpath %s) (subpath "/System") (subpath "/usr") (subpath "/bin") (subpath "/opt/homebrew") (subpath "/Library/Frameworks"))
(allow file-write* (subpath %s))`, strconv.Quote(root), strconv.Quote(root))
	args := append([]string{"-p", profile, "--"}, argv...)
	command := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", args...)
	command.Dir = cwd
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin", "HOME=" + root, "TMPDIR=" + root}
	for name, value := range env {
		command.Env = append(command.Env, name+"="+value)
	}
	stdout := &boundedCommandBuffer{}
	stderr := &boundedCommandBuffer{}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Start()
	if err == nil {
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case err = <-done:
		case <-ctx.Done():
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			err = <-done
		}
	}
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	} else if err != nil {
		exitCode = -1
	}
	result := workspaceCommandResult{
		Stdout: stdout.buffer.String(), Stderr: stderr.buffer.String(), ExitCode: exitCode,
		StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
	}
	if exitCode == -1 && result.Stdout == "" && result.Stderr == "" && ctx.Err() == nil {
		return result, errWorkspaceContainmentUnavailable
	}
	return result, err
}

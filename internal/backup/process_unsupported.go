//go:build !linux

package backup

import "os/exec"

// Windows needs suspended creation plus job assignment before child execution.
// A post-Start job assignment has an escape race and is not a safe fallback.
func processOwnershipSupported() error      { return ErrGitUnsupported }
func configureOwnedProcess(*exec.Cmd, bool) {}
func waitOwnedStart(*exec.Cmd) error        { return ErrGitUnsupported }
func releaseOwnedStart(*exec.Cmd) error     { return ErrGitUnsupported }
func terminateOwnedProcess(*exec.Cmd)       {}
func observeOwnedExit(*exec.Cmd) error      { return ErrGitUnsupported }

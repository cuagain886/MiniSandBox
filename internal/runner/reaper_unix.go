//go:build unix

package runner

// Orphan reaping belongs to sandbox-init. runnerd only waits for processes it
// starts directly, which avoids racing exec.Cmd.Wait.

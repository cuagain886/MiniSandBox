package domain

import "time"

type ExecutionSpec struct {
	Argv       []string
	Shell      string
	Env        map[string]string
	WorkingDir string
	Timeout    time.Duration
}

func (s ExecutionSpec) Valid() bool {
	return (len(s.Argv) > 0) != (s.Shell != "")
}

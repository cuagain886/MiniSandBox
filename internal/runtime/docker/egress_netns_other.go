//go:build !linux

package docker

import "errors"

type procNetNSResolver struct{}

func (procNetNSResolver) Identity(int) (string, error) {
	return "", errors.New("egress network namespaces require Linux")
}

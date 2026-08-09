// Package main 提供仅供 Docker integration 验收使用的静态 execution 探针，不承载生产运行时能力。
package main

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

const (
	exitConnectDenied = 20
	exitProbeFailure  = 21
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(exitProbeFailure)
	}
	switch os.Args[1] {
	case "socket-probe":
		probeSocket()
	case "argv":
		writeArgv(os.Args[2:])
	case "silent":
		return
	default:
		os.Exit(exitProbeFailure)
	}
}

func probeSocket() {
	if len(os.Args) != 4 {
		os.Exit(exitProbeFailure)
	}
	connection, err := net.DialTimeout("unix", os.Args[2], time.Second)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
			os.Exit(exitConnectDenied)
		}
		os.Exit(exitProbeFailure)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		os.Exit(exitProbeFailure)
	}
	request := "GET /healthz HTTP/1.1\r\nHost: runner\r\nConnection: close\r\n"
	if os.Args[3] != "-" {
		request += "Authorization: Bearer " + os.Args[3] + "\r\n"
	}
	if _, err := fmt.Fprint(connection, request+"\r\n"); err != nil {
		os.Exit(exitProbeFailure)
	}
	if _, err := bufio.NewReader(connection).ReadString('\n'); err != nil {
		os.Exit(exitProbeFailure)
	}
}

func writeArgv(arguments []string) {
	for _, argument := range arguments {
		encoded := base64.StdEncoding.EncodeToString([]byte(argument))
		if _, err := fmt.Fprintln(os.Stdout, strconv.Itoa(len(argument))+":"+encoded); err != nil {
			os.Exit(exitProbeFailure)
		}
	}
}

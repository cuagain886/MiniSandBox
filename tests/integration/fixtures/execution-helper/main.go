// Package main 提供仅供 Docker integration 验收使用的静态 execution 探针，不承载生产运行时能力。
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
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
	case "streams":
		writeStreams(os.Args[2:])
	case "exit":
		exitWithCode(os.Args[2:])
	case "process-tree":
		runProcessTree(os.Args[2:])
	case "tree-child":
		runTreeChild(os.Args[2:])
	case "tree-grandchild":
		runTreeGrandchild(os.Args[2:])
	case "marker":
		writeMarker(os.Args[2:])
	case "output-limit":
		writeOutputLimit(os.Args[2:])
	case "log-pages":
		writeLogPages(os.Args[2:])
	case "count-active-groups":
		countActiveGroups(os.Args[2:])
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

func writeStreams(arguments []string) {
	if len(arguments) != 1 {
		os.Exit(exitProbeFailure)
	}
	var stdout, stderr []byte
	switch arguments[0] {
	case "small":
		stdout, stderr = []byte("stdout-small"), []byte("stderr-small")
	case "large":
		stdout, stderr = bytes.Repeat([]byte("OUT-0123456789abcdef"), 8192), bytes.Repeat([]byte("ERR-fedcba9876543210"), 6144)
	case "binary":
		stdout, stderr = []byte{0x00, 0xff, 0x80, 'O', '\n'}, []byte{0xfe, 0x00, 0x81, 'E', '\r', '\n'}
	case "empty-stderr":
		stdout = []byte("stdout-only")
	case "interleaved":
		for index := 0; index < 128; index++ {
			if _, err := fmt.Fprintf(os.Stdout, "O%03d|", index); err != nil {
				os.Exit(exitProbeFailure)
			}
			if _, err := fmt.Fprintf(os.Stderr, "E%03d|", index); err != nil {
				os.Exit(exitProbeFailure)
			}
		}
		return
	default:
		os.Exit(exitProbeFailure)
	}
	if _, err := os.Stdout.Write(stdout); err != nil {
		os.Exit(exitProbeFailure)
	}
	if _, err := os.Stderr.Write(stderr); err != nil {
		os.Exit(exitProbeFailure)
	}
}

func exitWithCode(arguments []string) {
	if len(arguments) != 1 {
		os.Exit(exitProbeFailure)
	}
	code, err := strconv.Atoi(arguments[0])
	if err != nil || code < 0 || code > 255 {
		os.Exit(exitProbeFailure)
	}
	os.Exit(code)
}

func runProcessTree(arguments []string) {
	mode := treeMode(arguments)
	configureTreeSignal(mode)
	child := exec.Command(os.Args[0], "tree-child", mode)
	childOutput, err := child.StdoutPipe()
	if err != nil || child.Start() != nil {
		os.Exit(exitProbeFailure)
	}
	line, err := bufio.NewReader(childOutput).ReadString('\n')
	grandchildPID, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || parseErr != nil || grandchildPID <= 1 {
		os.Exit(exitProbeFailure)
	}
	if _, err := fmt.Fprintf(os.Stdout, "leader=%d child=%d grandchild=%d\n", os.Getpid(), child.Process.Pid, grandchildPID); err != nil {
		os.Exit(exitProbeFailure)
	}
	if mode == "race" || mode == "boundary" {
		_ = child.Wait()
		return
	}
	_ = child.Wait()
}

func runTreeChild(arguments []string) {
	mode := treeMode(arguments)
	configureTreeSignal(mode)
	grandchild := exec.Command(os.Args[0], "tree-grandchild", mode)
	if err := grandchild.Start(); err != nil {
		os.Exit(exitProbeFailure)
	}
	if _, err := fmt.Fprintln(os.Stdout, grandchild.Process.Pid); err != nil {
		os.Exit(exitProbeFailure)
	}
	_ = grandchild.Wait()
}

func runTreeGrandchild(arguments []string) {
	mode := treeMode(arguments)
	configureTreeSignal(mode)
	if mode == "race" || mode == "boundary" {
		duration := 150 * time.Millisecond
		if mode == "boundary" {
			duration = time.Second
		}
		time.Sleep(duration)
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func treeMode(arguments []string) string {
	if len(arguments) != 1 || arguments[0] != "term" && arguments[0] != "kill" && arguments[0] != "race" && arguments[0] != "boundary" {
		os.Exit(exitProbeFailure)
	}
	return arguments[0]
}

func configureTreeSignal(mode string) {
	if mode == "kill" {
		signal.Ignore(syscall.SIGTERM)
	}
}

func writeMarker(arguments []string) {
	if len(arguments) != 2 {
		os.Exit(exitProbeFailure)
	}
	delayMilliseconds, err := strconv.Atoi(arguments[1])
	if err != nil || delayMilliseconds < 0 || delayMilliseconds > 10_000 {
		os.Exit(exitProbeFailure)
	}
	time.Sleep(time.Duration(delayMilliseconds) * time.Millisecond)
	if err := os.WriteFile(arguments[0], []byte("background-complete"), 0o600); err != nil {
		os.Exit(exitProbeFailure)
	}
	_, _ = fmt.Fprint(os.Stdout, "marker-written")
}

func writeOutputLimit(arguments []string) {
	if len(arguments) != 3 {
		os.Exit(exitProbeFailure)
	}
	byteCount, err := strconv.Atoi(arguments[1])
	if err != nil || byteCount < 0 || byteCount > 1<<20 {
		os.Exit(exitProbeFailure)
	}
	write := func(file *os.File, size int, value byte) {
		if _, err := file.Write(bytes.Repeat([]byte{value}, size)); err != nil {
			os.Exit(exitProbeFailure)
		}
	}
	switch arguments[0] {
	case "stdout":
		write(os.Stdout, byteCount, 'O')
	case "stderr":
		write(os.Stderr, byteCount, 'E')
	case "combined":
		write(os.Stdout, byteCount/2, 'O')
		write(os.Stderr, byteCount-byteCount/2, 'E')
	default:
		os.Exit(exitProbeFailure)
	}
	if err := os.WriteFile(arguments[2], []byte("output-complete"), 0o600); err != nil {
		os.Exit(exitProbeFailure)
	}
}

func writeLogPages(arguments []string) {
	if len(arguments) != 1 {
		os.Exit(exitProbeFailure)
	}
	count, err := strconv.Atoi(arguments[0])
	if err != nil || count < 1 || count > 64 {
		os.Exit(exitProbeFailure)
	}
	for index := 0; index < count; index++ {
		file := os.Stdout
		prefix := "stdout"
		if index%2 != 0 {
			file = os.Stderr
			prefix = "stderr"
		}
		if _, err := fmt.Fprintf(file, "%s-%02d\n", prefix, index); err != nil {
			os.Exit(exitProbeFailure)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func countActiveGroups(arguments []string) {
	if len(arguments) != 1 {
		os.Exit(exitProbeFailure)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		os.Exit(exitProbeFailure)
	}
	count := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		command, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil || !bytes.Contains(command, []byte("process-tree\x00")) {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		closeParen := bytes.LastIndexByte(stat, ')')
		if closeParen < 0 {
			continue
		}
		fields := strings.Fields(string(stat[closeParen+1:]))
		if len(fields) < 3 || fields[2] != entry.Name() {
			continue
		}
		count++
	}
	if err := os.WriteFile(arguments[0], []byte(strconv.Itoa(count)), 0o600); err != nil {
		os.Exit(exitProbeFailure)
	}
}

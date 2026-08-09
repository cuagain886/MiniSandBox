package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"time"

	mobyclient "github.com/moby/moby/client"
	"minisandbox/internal/egressanchor"
	"minisandbox/internal/egresscontrol"
)

const maxDockerEgressPayload = egresscontrol.MaxResponseBytes + 4

type egressControlSession struct {
	attached mobyclient.ContainerAttachResult
	stdout   *dockerStdoutReader
	stop     func() bool
}

func openEgressControlSession(ctx context.Context, engine EgressEngine, containerID string, timeout time.Duration) (*egressControlSession, error) {
	if ctx == nil || engine == nil || containerID == "" || timeout <= 0 {
		return nil, errors.New("egress control attach input is invalid")
	}
	attached, err := engine.ContainerAttach(ctx, containerID, mobyclient.ContainerAttachOptions{
		Stream: true, Stdin: true, Stdout: true,
	})
	if err != nil {
		return nil, errors.New("attach egress control channel")
	}
	if attached.Conn == nil || attached.Reader == nil {
		attached.Close()
		return nil, errors.New("egress control channel is incomplete")
	}
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := attached.Conn.SetDeadline(deadline); err != nil {
		attached.Close()
		return nil, errors.New("set egress control deadline")
	}
	session := &egressControlSession{
		attached: attached,
		stdout:   &dockerStdoutReader{reader: attached.Reader},
	}
	session.stop = context.AfterFunc(ctx, func() { session.attached.Close() })
	return session, nil
}

func (session *egressControlSession) close() {
	if session == nil {
		return
	}
	if session.stop != nil {
		session.stop()
	}
	session.attached.Close()
}

func (session *egressControlSession) exchange(request egresscontrol.Request) (egressanchor.Attestation, error) {
	if session == nil || session.attached.Conn == nil || session.stdout == nil {
		return egressanchor.Attestation{}, errors.New("egress control session is incomplete")
	}
	framed, err := egresscontrol.EncodeRequest(request)
	if err != nil {
		return egressanchor.Attestation{}, err
	}
	if err := writeFull(session.attached.Conn, framed); err != nil {
		return egressanchor.Attestation{}, errors.New("write egress control request")
	}
	response, err := egresscontrol.ReadResponse(session.stdout)
	if err != nil {
		return egressanchor.Attestation{}, errors.New("read egress control response")
	}
	if response.RequestID != request.RequestID || response.Nonce != request.Nonce {
		return egressanchor.Attestation{}, errors.New("egress control response correlation mismatch")
	}
	if session.stdout.remaining != 0 {
		return egressanchor.Attestation{}, errors.New("egress control response has trailing data")
	}
	return response.Attestation, nil
}

type dockerStdoutReader struct {
	reader    *bufio.Reader
	remaining uint32
}

func (reader *dockerStdoutReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	if reader == nil || reader.reader == nil {
		return 0, errors.New("docker stdout reader is missing")
	}
	if reader.remaining == 0 {
		header := make([]byte, 8)
		if _, err := io.ReadFull(reader.reader, header); err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(header[4:])
		if header[0] != 1 || header[1] != 0 || header[2] != 0 || header[3] != 0 ||
			length == 0 || length > maxDockerEgressPayload {
			return 0, errors.New("docker egress stdout frame is invalid")
		}
		reader.remaining = length
	}
	if uint32(len(target)) > reader.remaining {
		target = target[:reader.remaining]
	}
	count, err := reader.reader.Read(target)
	reader.remaining -= uint32(count)
	return count, err
}

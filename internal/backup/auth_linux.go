package backup

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const credentialMaxConnections = 8

type linuxCredentialBroker struct {
	mu       sync.Mutex
	listener *net.UnixListener
	dir      string
	socket   string
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	done     chan struct{}
	wg       sync.WaitGroup
	conns    map[*net.UnixConn]struct{}
	group    int
	start    string
	repo     string
	username string
	secret   []byte
	exe      os.FileInfo
}

func newCredentialBrokerPlatform(o CredentialBrokerOptions) (credentialBrokerPlatform, error) {
	if !credentialSameExecutable(o.Executable) || !credentialPrivateDirectory(o.PrivateDir) {
		return nil, ErrCredentialDenied
	}
	dir, err := os.MkdirTemp(o.PrivateDir, "gitvault-broker-")
	if err != nil {
		return nil, ErrCredentialDenied
	}
	socket := filepath.Join(dir, "ipc") // random locator, NOT a bearer capability
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		_ = os.Remove(dir)
		return nil, ErrCredentialDenied
	}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = l.Close()
		_ = os.Remove(dir)
		return nil, ErrCredentialDenied
	}
	exe, _ := os.Stat(o.Executable)
	ctx, cancel := context.WithCancel(o.Context)
	b := &linuxCredentialBroker{listener: l, dir: dir, socket: socket, ctx: ctx, cancel: cancel, done: make(chan struct{}), conns: make(map[*net.UnixConn]struct{}), repo: o.AllowedRepository + ".git", username: o.Username, secret: append([]byte(nil), o.Secret...), exe: exe}
	b.wg.Add(1)
	go b.serve()
	go func() { <-ctx.Done(); _ = b.close() }()
	return b, nil
}

func credentialPrivateDirectory(path string) bool {
	i, err := os.Lstat(path)
	if err != nil || !i.IsDir() || i.Mode().Perm() != 0700 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return false
	}
	var st unix.Stat_t
	return unix.Stat(path, &st) == nil && st.Uid == uint32(os.Getuid())
}

// /proc stat's comm can contain spaces and parentheses. Fields after its LAST
// ')' are state, ppid, pgrp, ... starttime (field 22). Pin starttime to reject a
// recycled group leader. Permit only a live direct child, never our own group.
func credentialProcess(pid int) (ppid, group int, start string, err error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil || len(data) > 8192 {
		return 0, 0, "", ErrCredentialDenied
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, 0, "", ErrCredentialDenied
	}
	f := strings.Fields(string(data[end+1:]))
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return 0, 0, "", ErrCredentialDenied
	}
	ppid, e1 := strconv.Atoi(f[1])
	group, e2 := strconv.Atoi(f[2])
	if e1 != nil || e2 != nil {
		return 0, 0, "", ErrCredentialDenied
	}
	return ppid, group, f[19], nil
}

func (b *linuxCredentialBroker) permitGroup(pid int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.ctx.Err() != nil {
		return ErrCredentialBrokerClosed
	}
	parent, group, start, err := credentialProcess(pid)
	var st unix.Stat_t
	if err != nil || pid <= 0 || parent != os.Getpid() || group != pid || unix.Stat("/proc/"+strconv.Itoa(pid), &st) != nil || st.Uid != uint32(os.Getuid()) || b.group != 0 {
		return ErrCredentialDenied
	}
	b.group, b.start = group, start
	return nil
}

func (b *linuxCredentialBroker) locator() (string, int) { return b.socket, os.Getpid() }

func credentialPeer(c *net.UnixConn) (*unix.Ucred, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, ErrCredentialDenied
	}
	var peer *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || peerErr != nil || peer == nil {
		return nil, ErrCredentialDenied
	}
	return peer, nil
}

func (b *linuxCredentialBroker) authorized(peer *unix.Ucred) bool {
	if peer == nil || peer.Uid != uint32(os.Getuid()) || b.group == 0 || b.closed || b.ctx.Err() != nil {
		return false
	}
	parent, group, start, err := credentialProcess(b.group)
	if err != nil || parent != os.Getpid() || group != b.group || start != b.start {
		return false
	}
	_, peerGroup, _, err := credentialProcess(int(peer.Pid))
	i, statErr := os.Stat("/proc/" + strconv.Itoa(int(peer.Pid)) + "/exe")
	return err == nil && peerGroup == b.group && statErr == nil && os.SameFile(b.exe, i)
}

func (b *linuxCredentialBroker) serve() {
	defer b.wg.Done()
	for {
		c, err := b.listener.AcceptUnix()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed || len(b.conns) >= credentialMaxConnections {
			b.mu.Unlock()
			_ = c.Close()
			continue
		}
		b.conns[c] = struct{}{}
		b.wg.Add(1)
		b.mu.Unlock()
		go b.handle(c)
	}
}

func (b *linuxCredentialBroker) handle(c *net.UnixConn) {
	defer b.wg.Done()
	defer func() { _ = c.Close(); b.mu.Lock(); delete(b.conns, c); b.mu.Unlock() }()
	_ = c.SetDeadline(time.Now().Add(credentialConnectionTimeout))
	peer, err := credentialPeer(c)
	if err != nil {
		return
	}
	b.mu.Lock()
	allowed := b.authorized(peer)
	b.mu.Unlock()
	if !allowed {
		return
	}
	host, path, err := credentialRequest(c)
	if err != nil || host != "github.com" || path != b.repo {
		return
	}
	// Serialize final authorization and output with revocation/zeroing. Deadline
	// bounds a peer that stops draining; Close also closes its connection.
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.authorized(peer) {
		return
	}
	_, _ = io.WriteString(c, "username="+b.username+"\npassword=")
	_, _ = c.Write(b.secret)
	_, _ = io.WriteString(c, "\n\n")
}

func (b *linuxCredentialBroker) close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		<-b.done
		return nil
	}
	b.closed = true
	b.cancel()
	_ = b.listener.Close()
	for c := range b.conns {
		_ = c.Close()
	}
	b.mu.Unlock()
	b.wg.Wait()
	b.mu.Lock()
	clear(b.secret)
	b.secret = nil
	b.mu.Unlock()
	_ = os.Remove(b.socket)
	_ = os.Remove(b.dir) // exact owned directory only, never recursive removal
	close(b.done)
	return nil
}

func credentialHelperExchange(ctx context.Context, socket string, pid int, host, path string, out io.Writer) error {
	if !credentialPrivateDirectory(filepath.Dir(socket)) {
		return ErrCredentialDenied
	}
	var st unix.Stat_t
	if unix.Lstat(socket, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Mode&0777 != 0600 || st.Uid != uint32(os.Getuid()) {
		return ErrCredentialDenied
	}
	dialer := net.Dialer{Timeout: credentialConnectionTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return ErrCredentialDenied
	}
	c := conn.(*net.UnixConn)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(credentialConnectionTimeout))
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	peer, err := credentialPeer(c)
	if err != nil || peer.Uid != uint32(os.Getuid()) || int(peer.Pid) != pid {
		return ErrCredentialDenied
	}
	if _, err := io.WriteString(c, credentialWireRequest(host, path)); err != nil {
		return ErrCredentialDenied
	}
	data, err := io.ReadAll(io.LimitReader(c, credentialSecretLimit+512))
	defer clear(data)
	if err != nil || len(data) == 0 || len(data) >= credentialSecretLimit+512 || !bytes.HasPrefix(data, []byte("username=")) || !bytes.HasSuffix(data, []byte("\n\n")) || ctx.Err() != nil {
		return ErrCredentialDenied
	}
	n, err := out.Write(data)
	if err != nil || n != len(data) {
		return ErrCredentialDenied
	}
	return nil
}

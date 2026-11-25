//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func openLinuxPTY(t *testing.T) (master *os.File, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("skipping PTY test: cannot open /dev/ptmx: %v", err)
	}

	var ptn uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&ptn))); errno != 0 {
		_ = m.Close()
		t.Skipf("skipping PTY test: TIOCGPTN failed: %v", errno)
	}

	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = m.Close()
		t.Skipf("skipping PTY test: TIOCSPTLCK failed: %v", errno)
	}

	slavePath := fmt.Sprintf("/dev/pts/%d", ptn)
	s, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		t.Skipf("skipping PTY test: cannot open %s: %v", slavePath, err)
	}
	return m, s
}

func getTermios(t *testing.T, f *os.File) syscall.Termios {
	t.Helper()
	var term syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&term))); errno != 0 {
		t.Fatalf("TCGETS failed: %v", errno)
	}
	return term
}

func TestSetupInteractiveRealPTY(t *testing.T) {
	env := newTestRepo(t)
	binPath := ensureTestBinary(t)

	virtHome := filepath.Join(env.rootDir, "pty-home")
	if err := os.MkdirAll(filepath.Join(virtHome, ".gemini"), 0o755); err != nil {
		t.Fatalf("mkdir .gemini: %v", err)
	}
	// Note: ~/.codex is absent initially!

	t.Run("select_undetected_deselect_detected_and_install", func(t *testing.T) {
		master, slave := openLinuxPTY(t)
		defer master.Close()
		defer slave.Close()

		initialTerm := getTermios(t, slave)

		cmd := exec.Command(binPath, "setup", "--no-color")
		cmd.Dir = env.repoDir
		cmd.Env = append(os.Environ(), "HOME="+virtHome, "NO_COLOR=1", "DSH_HOME=")
		cmd.Stdin = slave
		cmd.Stdout = slave
		cmd.Stderr = slave

		if err := cmd.Start(); err != nil {
			t.Fatalf("start git-by setup in PTY: %v", err)
		}

		outCh := make(chan string, 1)
		readyCh := make(chan struct{}, 1)
		go func() {
			var all bytes.Buffer
			buf := make([]byte, 1024)
			notified := false
			for {
				n, err := master.Read(buf)
				if n > 0 {
					all.Write(buf[:n])
					if !notified && bytes.Contains(all.Bytes(), []byte("1 of 8 selected")) {
						notified = true
						readyCh <- struct{}{}
					}
				}
				if err != nil {
					break
				}
			}
			outCh <- all.String()
		}()

		select {
		case <-readyCh:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("timed out waiting for initial PTY checklist frame")
		}

		// Row 0 is Antigravity (pre-selected [x]).
		// Press Space to uncheck Antigravity, Down x3 to Codex (index 3), Space to check Codex,
		// Down x2 to DeepSeek Harness (index 5), Space to check DeepSeek Harness, Enter to install!
		if _, err := master.Write([]byte(" \x1b[B\x1b[B\x1b[B \x1b[B\x1b[B \r")); err != nil {
			t.Fatalf("write keystrokes to PTY: %v", err)
		}

		waitErr := cmd.Wait()
		afterTerm := getTermios(t, slave)
		_ = slave.Close()
		fullOut := <-outCh

		if waitErr != nil {
			t.Fatalf("git-by setup exited with error: %v\noutput:\n%s", waitErr, fullOut)
		}
		if afterTerm.Lflag != initialTerm.Lflag || afterTerm.Iflag != initialTerm.Iflag {
			t.Fatalf("termios not restored after Enter: got Lflag=%#x Iflag=%#x, want Lflag=%#x Iflag=%#x",
				afterTerm.Lflag, afterTerm.Iflag, initialTerm.Lflag, initialTerm.Iflag)
		}
		if !strings.Contains(fullOut, "[x] Antigravity (agy)") ||
			!strings.Contains(fullOut, "[ ] Codex (codex)") ||
			!strings.Contains(fullOut, "[ ] DeepSeek Harness (dsh)") {
			t.Fatalf("initial checklist missing expected pre-selections:\n%s", fullOut)
		}
		if !strings.Contains(fullOut, "Configured 2 agent(s).") {
			t.Fatalf("expected final summary for 2 configured agents:\n%s", fullOut)
		}

		// Antigravity was unchecked -> must NOT exist; Codex and DeepSeek Harness were checked -> MUST exist!
		if _, err := os.Stat(filepath.Join(virtHome, ".gemini", "config", "rules", "git-by.md")); !os.IsNotExist(err) {
			t.Fatalf("unchecked Antigravity rule file should not exist, err=%v", err)
		}
		codexRule := filepath.Join(virtHome, ".codex", "AGENTS.md")
		if b, err := os.ReadFile(codexRule); err != nil || !strings.Contains(string(b), "--harness codex") {
			t.Fatalf("expected %s with codex harness, err=%v", codexRule, err)
		}
		dshRule := filepath.Join(virtHome, ".dsh", "AGENTS.md")
		if b, err := os.ReadFile(dshRule); err != nil || !strings.Contains(string(b), "--harness dsh") {
			t.Fatalf("expected %s with dsh harness, err=%v", dshRule, err)
		}
	})

	t.Run("standalone_esc_aborts_without_closing_pty_and_restores_termios", func(t *testing.T) {
		master, slave := openLinuxPTY(t)
		defer master.Close()
		defer slave.Close()

		initialTerm := getTermios(t, slave)
		escHome := filepath.Join(env.rootDir, "pty-esc-home")
		if err := os.MkdirAll(filepath.Join(escHome, ".gemini"), 0o755); err != nil {
			t.Fatalf("mkdir .gemini: %v", err)
		}

		cmd := exec.Command(binPath, "setup", "--no-color")
		cmd.Dir = env.repoDir
		cmd.Env = append(os.Environ(), "HOME="+escHome, "NO_COLOR=1")
		cmd.Stdin = slave
		cmd.Stdout = slave
		cmd.Stderr = slave

		if err := cmd.Start(); err != nil {
			t.Fatalf("start git-by setup in PTY: %v", err)
		}

		readyCh := make(chan struct{}, 1)
		outCh := make(chan string, 1)
		go func() {
			var all bytes.Buffer
			buf := make([]byte, 1024)
			notified := false
			for {
				n, err := master.Read(buf)
				if n > 0 {
					all.Write(buf[:n])
					if !notified && bytes.Contains(all.Bytes(), []byte("Antigravity (agy)")) {
						notified = true
						readyCh <- struct{}{}
					}
				}
				if err != nil {
					break
				}
			}
			outCh <- all.String()
		}()

		select {
		case <-readyCh:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("timed out waiting for PTY prompt")
		}

		// Send ONLY standalone ESC (0x1b) with slave/master still open — must exit cleanly within VTIME!
		if _, err := master.Write([]byte{0x1b}); err != nil {
			t.Fatalf("write ESC to PTY: %v", err)
		}

		doneCh := make(chan error, 1)
		go func() { doneCh <- cmd.Wait() }()

		select {
		case err := <-doneCh:
			if err != nil {
				t.Fatalf("expected exit 0 on standalone Esc, got %v", err)
			}
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("git-by setup hung on standalone Esc in real PTY!")
		}

		afterTerm := getTermios(t, slave)
		_ = slave.Close()
		fullOut := <-outCh

		if afterTerm.Lflag != initialTerm.Lflag || afterTerm.Iflag != initialTerm.Iflag {
			t.Fatalf("termios not restored after Esc: got Lflag=%#x, want %#x", afterTerm.Lflag, initialTerm.Lflag)
		}
		if !strings.Contains(fullOut, "Setup cancelled; nothing changed.") {
			t.Fatalf("expected cancellation message, got:\n%s", fullOut)
		}
		if _, err := os.Stat(filepath.Join(escHome, ".gemini", "config")); !os.IsNotExist(err) {
			t.Fatalf("Esc cancellation unexpectedly created files in %s", escHome)
		}
	})
}

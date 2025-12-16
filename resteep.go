package resteep

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/term"
)

func Resteep(run func(state []byte, stateCh chan<- []byte) error) error {
	stateB64, hasData := os.LookupEnv("RESTEEP_STATE")

	if !hasData {
		return supervisor()
	}

	// Otherwise, we're the subprocess

	state, err := base64.StdEncoding.DecodeString(stateB64)
	if err != nil {
		return fmt.Errorf("failed to decode RESTEEP_STATE: %w", err)
	}

	// Open fd 3 for writing state updates back to supervisor
	statePipe := os.NewFile(3, "statepipe")
	if statePipe == nil {
		return fmt.Errorf("failed to open fd 3 for state updates")
	}

	stateCh := msgChanFromWriter(statePipe)
	defer close(stateCh)

	err = run(state, stateCh)
	if err != nil {
		return fmt.Errorf("failed to run: %w", err)
	}

	return nil
}

func supervisor() error {
	originalTermState, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to get terminal state: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), originalTermState)

	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	changeCh, closeWatcher, err := watchGoFilesInDir(root)
	if err != nil {
		return fmt.Errorf("failed to watch go files in dir: %w", err)
	}
	defer closeWatcher()

	// Create a new pipe for custom communication
	// This pipe lives for the entire supervisor lifetime, so we don't close it
	// explicitly.
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to create pipe: %w", err)
	}

	goPath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found in PATH: %w", err)
	}

	// Check if stdin is a terminal for foreground process group management
	isTerminal := term.IsTerminal(int(os.Stdin.Fd()))

	args := []string{"run"}

	// Preserve build flags
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, setting := range info.Settings {
			// Build settings are either flags, which start with "-", or environment
			// variables which are inherited automatically.
			if strings.HasPrefix(setting.Key, "-") {
				args = append(args, setting.Key, setting.Value)
			}
		}
	}

	var currentState []byte
	var cmd *exec.Cmd

	// Create the message channel once, outside the loop
	msgCh := msgChanFromReader(r)

	// Signal channel for Ctrl-C when no child is running
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Cleanup function to kill the child process and its descendants
	cleanUpChild := func() {
		if cmd != nil && cmd.Process != nil {
			// Kill the entire process group
			pgid, err := syscall.Getpgid(cmd.Process.Pid)
			if err == nil {
				// Send SIGKILL first for graceful shutdown
				err := syscall.Kill(-pgid, syscall.SIGKILL)
				if err != nil {
					log.Printf("Failed to send SIGKILL to process group: %v", err)
				}
			} else {
				// Fallback to killing just the process
				cmd.Process.Signal(syscall.SIGKILL)
			}
			// Note: Don't call cmd.Wait() here - the exitCh goroutine will handle it
		}
	}

	// Ensure cleanup happens when supervisor exits
	defer cleanUpChild()

	for {
		cmd = exec.Command(goPath, append(args, getMainPackage())...)

		encodedData := base64.StdEncoding.EncodeToString(currentState)
		cmd.Env = setEnvVar(os.Environ(), "RESTEEP_STATE", encodedData)

		// Inherit standard streams
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		// Pass the write end as fd 3
		cmd.ExtraFiles = []*os.File{w}

		// Set process group ID so we can kill the entire group
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}

		err = cmd.Start()
		if err != nil {
			return fmt.Errorf("failed to start subprocess: %w", err)
		}

		// Transfer terminal foreground control to the child process group
		if isTerminal {
			pgid, err := syscall.Getpgid(cmd.Process.Pid)
			if err == nil {
				// Make the child's process group the foreground process group
				// This allows it to receive terminal signals (Ctrl-C, etc.)
				_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TIOCSPGRP, uintptr(unsafe.Pointer(&pgid)))
				if errno != 0 {
					log.Printf("Warning: failed to set foreground process group: %v", errno)
				}
			} else {
				log.Printf("Failed to get child pgid: %v", err)
			}
		}

		// Wait for child to exit in a goroutine
		exitCh := make(chan error, 1)
		go func() {
			exitCh <- cmd.Wait()
		}()

		for cmd != nil {
			select {
			case msgData, ok := <-msgCh:
				if !ok {
					return fmt.Errorf("subprocess pipe closed unexpectedly")
				}
				currentState = msgData

			case _ = <-changeCh:
				// Kill the subprocess and its descendants
				cleanUpChild()
				// Wait for the process to fully exit
				<-exitCh

				// Reclaim terminal foreground before starting new child
				// We need to ignore SIGTTOU while doing this, otherwise we'll be suspended
				if isTerminal {
					// Temporarily ignore SIGTTOU so we can manipulate terminal foreground
					signal.Ignore(syscall.SIGTTOU)
					defer signal.Reset(syscall.SIGTTOU)

					supervisorPgid := syscall.Getpgrp()
					_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TIOCSPGRP, uintptr(unsafe.Pointer(&supervisorPgid)))
					if errno != 0 {
						log.Printf("Warning: failed to reclaim foreground: %v", errno)
					}

					term.Restore(int(os.Stdin.Fd()), originalTermState)
				}

				cmd = nil // Break to outer loop to restart

			case err := <-exitCh:
				term.Restore(int(os.Stdin.Fd()), originalTermState)

				// Child process exited naturally
				var exitCode int
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else if err != nil {
					return fmt.Errorf("subprocess failed: %w", err)
				}

				// Reclaim foreground so we can receive Ctrl-C
				if isTerminal {
					signal.Ignore(syscall.SIGTTOU)
					supervisorPgid := syscall.Getpgrp()
					syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TIOCSPGRP, uintptr(unsafe.Pointer(&supervisorPgid)))
					signal.Reset(syscall.SIGTTOU)
				}

				fmt.Printf("\n\nProcess exited with code %d.\nPress Ctrl-C to exit or save a file to reload.\n", exitCode)

				// Wait for either file change or Ctrl-C
				select {
				case <-changeCh:
					// File changed, restart child (break to outer loop)
					cmd = nil
				case <-sigCh:
					// Ctrl-C pressed, exit supervisor
					return nil
				}
			}
		}
	}
}

func msgChanFromWriter(w io.WriteCloser) chan<- []byte {
	ch := make(chan []byte, 1)

	go func() {
		defer w.Close()
		for newState := range ch {
			// Write length prefix (4 bytes, big endian)
			length := uint32(len(newState))
			if err := binary.Write(w, binary.BigEndian, length); err != nil {
				return
			}
			// Write state data
			if _, err := w.Write(newState); err != nil {
				return
			}
		}
	}()

	return ch
}

func msgChanFromReader(r io.Reader) <-chan []byte {
	ch := make(chan []byte)

	go func() {
		defer close(ch)
		for {
			var length uint32
			err := binary.Read(r, binary.BigEndian, &length)
			if err != nil {
				log.Printf("Failed to read state length: %v", err)
				return
			}
			opaqueData := make([]byte, length)
			_, err = io.ReadFull(r, opaqueData)
			if err != nil {
				return
			}
			ch <- opaqueData
		}
	}()

	return ch
}

func watchGoFilesInDir(dir string) (<-chan fsnotify.Event, func() error, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}

	if err := addDirs(watcher, dir); err != nil {
		return nil, nil, err
	}

	changeCh := make(chan fsnotify.Event)

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Check if it's a .go file
				if filepath.Ext(event.Name) == ".go" && (event.Op.Has(fsnotify.Write) || event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Remove)) {
					changeCh <- event
				}

				// If a new directory was created, watch it too
				if event.Op.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						watcher.Add(event.Name)
					}
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("Watcher error: %v", err)
			}
		}
	}()

	return changeCh, watcher.Close, nil
}

// addDirs adds all directories under root to the watcher, skipping hidden dirs
// (`.*`) and `vendor`.
func addDirs(watcher *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden directories and vendor
		if info.IsDir() {
			name := info.Name()
			if name[0] == '.' || name == "vendor" {
				return filepath.SkipDir
			}

			// Add directory to watcher
			if err := watcher.Add(path); err != nil {
				return err
			}
		}

		return nil
	})
}

// setEnvVar removes any existing entry for key and adds key=value
func setEnvVar(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	prefix := key + "="

	// Copy all environment variables except the one we're setting
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}

	// Add our variable at the end
	result = append(result, prefix+value)
	return result
}

// getMainPackage returns the directory containing the main function at the top
// of the call stack. This must be called from the main goroutine.
func getMainPackage() string {
	pcs := make([]uintptr, 10)
	n := runtime.Callers(0, pcs)

	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if frame.Function == "main.main" {
			// Return the directory containing main.go
			return filepath.Dir(frame.File)
		}
		if !more {
			break
		}
	}
	panic("getMainPackage must be called from the main goroutine")
}

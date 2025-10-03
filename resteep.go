package resteep

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/fsnotify/fsnotify"
)

type ResteepableModel interface {
	tea.Model
	Marshal() ([]byte, error)
}

type reloadMsg struct{}

type wrapperModel struct {
	inner             ResteepableModel
	mainPackage       string
	input             *os.File
	previousTermState *term.State
}

func (m wrapperModel) Init() tea.Cmd {
	return m.inner.Init()
}

func (m wrapperModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case reloadMsg:
		modelData, err := m.inner.Marshal()
		if err != nil {
			panic(fmt.Sprintf("failed to marshal model: %v", err))
		}

		term.Restore(os.Stdin.Fd(), m.previousTermState)
		if err := reload(m.mainPackage, modelData); err != nil {
			panic(fmt.Sprintf("failed to reload: %v", err))
		}

		return nil, nil // never reached
	}

	newInner, cmd := m.inner.Update(msg)
	m.inner = newInner.(ResteepableModel)
	return m, cmd
}

func (m wrapperModel) View() string {
	return m.inner.View()
}

func Resteep(createModel func([]byte) ResteepableModel, opts ...tea.ProgramOption) (tea.Model, error) {
	mainPackage := getMainPackage()

	var model ResteepableModel
	if os.Getenv("RESTEEP_MODEL") == "" {
		model = createModel(nil)
	} else {
		data, err := base64.StdEncoding.DecodeString(os.Getenv("RESTEEP_MODEL"))
		if err != nil {
			return nil, fmt.Errorf("failed to decode RESTEEP_MODEL: %w", err)
		}
		model = createModel(data)
	}

	// We're assuming stdin is a tty here. Bubble Tea is more flexible than that,
	// but we're not handling that yet.
	input := os.Stdin

	// Save the current terminal state so we can restore it just before reloading.
	// Bubble Tea will restore state itself on exit, but allowing the Bubble Tea
	// program to completely exit is too slow, and causes a flicker.
	previousTermState, err := term.GetState(input.Fd())
	if err != nil {
		return nil, fmt.Errorf("failed to get terminal state: %w", err)
	}

	program := tea.NewProgram(
		wrapperModel{
			inner:             model,
			mainPackage:       mainPackage,
			input:             input,
			previousTermState: previousTermState,
		},
		opts...,
	)

	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	closeWatcher, err := watchGoFilesInDir(root, func() error {
		program.Send(reloadMsg{})
		return nil
	})
	if err != nil {
		return nil, err
	}
	defer closeWatcher()

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}

	return finalModel.(wrapperModel).inner, nil
}

func watchGoFilesInDir(dir string, cb func() error) (func() error, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := addDirs(watcher, dir); err != nil {
		return nil, err
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// Check if it's a .go file
				if filepath.Ext(event.Name) == ".go" && (event.Op.Has(fsnotify.Write) || event.Op.Has(fsnotify.Create) || event.Op.Has(fsnotify.Remove)) {
					if err := cb(); err != nil {
						log.Println("Error reloading:", err)
					}
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
				log.Fatal("Error:", err)
			}
		}
	}()

	return watcher.Close, nil
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

func reload(mainPackage string, modelData []byte) error {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found in PATH: %w", err)
	}

	args := []string{"go", "run"}

	// Preserve build tags
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, setting := range info.Settings {
			if setting.Key == "-tags" {
				args = append(args, "-tags", setting.Value)
			}
		}
	}

	encodedModelData := base64.StdEncoding.EncodeToString(modelData)
	err = syscall.Exec(goPath, append(args, mainPackage), append(os.Environ(), fmt.Sprintf("RESTEEP_MODEL=%s", encodedModelData)))
	if err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	return nil // never reached
}

// getMainPackage returns the directory containing the main function at the top
// of the call stack. This must be
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

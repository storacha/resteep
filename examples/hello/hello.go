package main

import (
	"log"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/storacha/resteep"
)

type model struct {
	text string
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyRunes {
			m.text += msg.String()
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() string {
	return "Hello, World!\n\n" + m.text + "\n\nPress ctrl-c to quit.\n"
}

func (m model) Marshal() ([]byte, error) {
	return []byte(m.text), nil
}

func main() {
	_, err := resteep.Resteep(
		func(b []byte) resteep.ResteepableModel {
			if b == nil {
				return model{}
			}
			return model{text: string(b)}
		},
		tea.WithAltScreen(),
	)
	if err != nil {
		log.Fatal(err)
	}
}

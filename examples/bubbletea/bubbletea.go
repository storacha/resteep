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
	return "Type some text below:\n\n> " + m.text + "\n\nNow change this text in bubbletea.go and see it update here.\n\nPress ctrl-c to quit.\n"
}

func main() {
	err := resteep.Resteep(
		resteep.RunBubbleTea(
			func(state []byte) (model, error) {
				var m model
				if state != nil {
					m.text = string(state)
				}
				return m, nil
			},
			func(m model) ([]byte, error) {
				return []byte(m.text), nil
			},
			tea.WithAltScreen(),
		),
	)
	if err != nil {
		log.Fatalln(err)
	}
}

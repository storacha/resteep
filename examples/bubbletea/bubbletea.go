package main

import (
	"fmt"
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

type teaBag[M tea.Model] struct {
	wrapped M
	marshal func(M) ([]byte, error)
	stateCh chan<- []byte
}

func (m teaBag[M]) Init() tea.Cmd {
	return m.wrapped.Init()
}

func (m teaBag[M]) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updatedModel, cmd := m.wrapped.Update(msg)
	correctModel, ok := updatedModel.(M)
	if !ok {
		log.Fatalf("expected %T model after update, but got %T\n", correctModel, updatedModel)
	}
	m.wrapped = correctModel
	if stateBytes, err := m.marshal(correctModel); err == nil {
		m.stateCh <- stateBytes
	}
	return m, cmd
}

func (m teaBag[M]) View() string {
	return m.wrapped.View()
}

func main() {
	err := resteep.Resteep(
		func(state []byte, stateCh chan<- []byte) error {
			var m model
			if state != nil {
				m.text = string(state)
			}

			p := tea.NewProgram(teaBag[model]{
				wrapped: m,
				marshal: func(m model) ([]byte, error) {
					return []byte(m.text), nil
				},
				stateCh: stateCh,
			}, tea.WithAltScreen())

			_, err := p.Run()
			if err != nil {
				log.Fatalln(fmt.Errorf("Bubble Tea program failed: %w", err))
			}
			return nil
		},
	)
	if err != nil {
		log.Fatalln(err)
	}
}

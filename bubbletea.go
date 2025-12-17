package resteep

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// RunBubbleTea returns a run function for [resteep.Resteep] that runs a Bubble
// Tea program. The makeModel function creates the initial Bubble Tea model, and
// receives nil or, if reloading, the previous state bytes. The marshalModel
// function converts the current model to bytes which can restore the model
// after reload.
func RunBubbleTea[M tea.Model](
	makeModel func(state []byte) (M, error),
	marshalModel func(M) ([]byte, error),
	opts ...tea.ProgramOption,
) func(state []byte, stateCh chan<- []byte) error {
	return func(state []byte, stateCh chan<- []byte) error {
		m, err := makeModel(state)
		if err != nil {
			return fmt.Errorf("failed to create Bubble Tea model: %w", err)
		}

		p := tea.NewProgram(teaBag[M]{
			model:   m,
			marshal: marshalModel,
			stateCh: stateCh,
		}, opts...)

		_, err = p.Run()
		if err != nil {
			return fmt.Errorf("Bubble Tea program failed: %w", err)
		}
		return nil
	}
}

type teaBag[M tea.Model] struct {
	model   M
	marshal func(M) ([]byte, error)
	stateCh chan<- []byte
}

func (m teaBag[M]) Init() tea.Cmd {
	return m.model.Init()
}

func (m teaBag[M]) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updatedModel, cmd := m.model.Update(msg)
	correctModel, ok := updatedModel.(M)
	if !ok {
		panic(fmt.Sprintf("expected %T model after update, but got %T\n", correctModel, updatedModel))
	}
	m.model = correctModel
	if stateBytes, err := m.marshal(correctModel); err == nil {
		m.stateCh <- stateBytes
	}
	return m, cmd
}

func (m teaBag[M]) View() string {
	return m.model.View()
}

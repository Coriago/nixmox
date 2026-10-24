package terminal

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"nixmox/internal/client"
	"nixmox/internal/ui/views"
)

func Run(ctx context.Context, api *client.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan views.Update, 8)
	connection := views.NewConnection(api)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection.Run(ctx, func(u views.Update) {
			select {
			case updates <- u:
			case <-ctx.Done():
			}
		})
	}()
	defer func() { cancel(); <-done }()
	m := New(views.New())
	m.connection, m.ctx, m.updates = connection, ctx, updates
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}
func (m *Model) waitUpdate() tea.Cmd {
	if m.connection == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case update := <-m.updates:
			return update
		case <-m.ctx.Done():
			return nil
		}
	}
}
func (m *Model) exchange() {
	if m.connection == nil {
		return
	}
	if selection := m.Core.Selection(); selection != m.selection {
		m.selection = selection
		m.connection.Select(selection)
	}
	if command := m.Core.TakeCommand(); command != nil {
		m.connection.Submit(*command)
	}
}

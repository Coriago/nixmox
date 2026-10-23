package views

import "fmt"

func (m *Model) mockClock() string {
	seconds := 31 + int(m.elapsed.Seconds())
	return fmt.Sprintf("14:%02d:%02d", 32+seconds/60, seconds%60)
}
func (m *Model) hostActionMock(kind string) {
	h := m.Host(m.HostID)
	if h == nil || h.Reachability != "online" {
		return
	}
	if kind == "refresh" {
		m.Notice = "Refreshed mock observations for " + h.Name + " · no daemon contacted."
		return
	}
	if len(m.Tasks) >= 128 {
		m.Notice = "Preview task limit reached. Replay to reset."
		return
	}
	t := Task{ID: fmt.Sprintf("network-%s-%d", h.ID, len(m.Tasks)), Name: "Network check", HostID: h.ID, State: "running", Phase: "Reading interface observations", Total: 24, Started: m.mockClock()}
	t.Log.Append("INFO  mock network check admitted · no host contacted")
	m.Tasks = append(m.Tasks, t)
	m.Notice = "Network check added to " + h.Name + " task history · simulated."
}

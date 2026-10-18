package installerui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

type fakeNetwork struct {
	devices                         []Device
	ready                           bool
	scans, connects, enables, waits int
	password                        string
	failConnect                     bool
}

func (f *fakeNetwork) Wait(context.Context)                      { f.waits++ }
func (f *fakeNetwork) Devices(context.Context) ([]Device, error) { return f.devices, nil }
func (f *fakeNetwork) Ready(Device) bool                         { return f.ready }
func (f *fakeNetwork) Scan(context.Context) ([]Network, error) {
	f.scans++
	return []Network{{BSSID: "aa:bb:cc:dd:ee:ff", SSID: "Test:WiFi\x1b[2J", Signal: "80", Security: "WPA2", Device: "wlan0"}}, nil
}
func (f *fakeNetwork) Connect(_ context.Context, n Network, in io.Reader, _ io.Writer) error {
	f.connects++
	if n.BSSID != "aa:bb:cc:dd:ee:ff" || n.Device != "wlan0" {
		return fmt.Errorf("wrong network")
	}
	if f.failConnect {
		f.failConnect = false
		return fmt.Errorf("cancelled")
	}
	var err error
	f.password, err = readChoice(in)
	return err
}
func (f *fakeNetwork) EnableWiFi(context.Context) error { f.enables++; return nil }

func TestAutomaticNetworkingDoesNotPrompt(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		ready   bool
		message string
	}{
		{"ethernet", true, "Wired connection ready"},
		{"wifi", true, "Wi-Fi connection ready"},
		{"loopback", false, "No Wi-Fi adapter found"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			backend := &fakeNetwork{devices: []Device{{Name: "device0", Kind: tc.kind}}, ready: tc.ready}
			var out bytes.Buffer
			if err := Run(context.Background(), backend, strings.NewReader(""), &out, true, nil); err != nil {
				t.Fatal(err)
			}
			if backend.waits != 1 || backend.scans != 0 || !strings.Contains(out.String(), tc.message) {
				t.Fatal(out.String(), backend)
			}
		})
	}
}

func TestMenuConnectRetryStatusAndReturn(t *testing.T) {
	backend := &fakeNetwork{devices: []Device{{"wlan0", "wifi"}}, failConnect: true}
	var out bytes.Buffer
	statusCalls := 0
	status := func(context.Context) ([]byte, error) { statusCalls++; return []byte(`{"status":"temporary"}`), nil }
	// Password must remain unread until the selected backend takes over stdin.
	in := strings.NewReader("invalid\ne\nr\ns\n1\n1\nnot-a-real-password\nq\n")
	if err := Run(context.Background(), backend, in, &out, true, status); err != nil {
		t.Fatal(err)
	}
	if backend.connects != 2 || backend.enables != 1 || statusCalls != 1 || backend.password != "not-a-real-password" {
		t.Fatal(backend, statusCalls)
	}
	for _, text := range []string{"Connection failed", "Connected.", "temporary", "Return to console"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing", text, out.String())
		}
	}
	if strings.Contains(out.String(), backend.password) || strings.Contains(out.String(), "\x1b") {
		t.Fatal("secret or SSID terminal control leaked")
	}
}

func TestMenuEOFReturnsToConsole(t *testing.T) {
	backend := &fakeNetwork{}
	if err := Run(context.Background(), backend, strings.NewReader(""), io.Discard, false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkManagerEscapedFields(t *testing.T) {
	got := fields(`*:AA\:BB\:CC\:DD\:EE\:FF:Office\: WiFi\\guest:75:WPA2:wlan0`)
	if len(got) != 6 || got[1] != "AA:BB:CC:DD:EE:FF" || got[2] != `Office: WiFi\guest` {
		t.Fatal(got)
	}
}

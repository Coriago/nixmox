package power

import "testing"

func TestPacket(t *testing.T) {
	b, err := Packet("01:23:45:67:89:ab")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 102 {
		t.Fatal(len(b))
	}
	for i := 0; i < 6; i++ {
		if b[i] != 255 {
			t.Fatal(b[:6])
		}
	}
	for i := 6; i < len(b); i += 6 {
		if b[i] != 1 || b[i+5] != 171 {
			t.Fatal(b[i : i+6])
		}
	}
	for _, mac := range []string{"", "bogus", "00:11:22:33:44:55:66:77"} {
		if _, err := Packet(mac); err == nil {
			t.Fatal(mac)
		}
	}
}

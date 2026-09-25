package adb

import "testing"

func TestParseDevices(t *testing.T) {
	out := `List of devices attached
R58M123ABC             device usb:1-1 product:beyond1 model:SM_G973F device:beyond1 transport_id:1
192.168.1.40:37421     device product:panther model:Pixel_7 device:panther transport_id:2
emulator-5554          unauthorized transport_id:3
`
	d := parseDevices(out)
	if len(d) != 3 || d[0].Model != "SM G973F" || d[0].Wireless || !d[1].Wireless || d[2].State != "unauthorized" {
		t.Fatalf("%+v", d)
	}
}

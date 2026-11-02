package redfishx

import (
	"reflect"
	"testing"
)

func TestMediaBootTargetCandidates(t *testing.T) {
	tests := []struct {
		name         string
		requested    string
		allowed      []string
		manufacturer string
		want         []string
	}{
		{
			name:         "hpe allowable Cd only",
			allowed:      []string{"None", "Pxe", "Cd", "Hdd"},
			manufacturer: "HPE",
			want:         []string{"Cd"},
		},
		{
			name:         "supermicro prefers UsbCd when both listed",
			allowed:      []string{"None", "Pxe", "Hdd", "Cd", "UsbCd", "Usb"},
			manufacturer: "Supermicro",
			want:         []string{"UsbCd", "Cd", "Usb"},
		},
		{
			name:         "supermicro manufacturer case-insensitive",
			allowed:      []string{"UsbCd", "Cd"},
			manufacturer: "SUPERMICRO",
			want:         []string{"UsbCd", "Cd"},
		},
		{
			name:         "non-super with both still prefers Cd",
			allowed:      []string{"Cd", "UsbCd"},
			manufacturer: "Dell Inc.",
			want:         []string{"Cd", "UsbCd"},
		},
		{
			name:         "empty allowable supermicro uses super pref",
			manufacturer: "Supermicro",
			want:         []string{"UsbCd", "UefiCd", "Cd", "Usb", "RemoteDrive"},
		},
		{
			name: "empty allowable default prefers Cd",
			want: []string{"Cd", "UsbCd", "UefiCd", "Usb", "RemoteDrive"},
		},
		{
			name:         "explicit request first when allowed",
			requested:    "Usb",
			allowed:      []string{"Cd", "UsbCd", "Usb"},
			manufacturer: "Supermicro",
			want:         []string{"Usb", "UsbCd", "Cd"},
		},
		{
			name:         "explicit request not in allowed still tried first",
			requested:    "Floppy",
			allowed:      []string{"Cd", "UsbCd"},
			manufacturer: "Supermicro",
			want:         []string{"Floppy", "UsbCd", "Cd"},
		},
		{
			name:    "allowable case preserved from BMC",
			allowed: []string{"USBCD", "CD"},
			want:    []string{"CD", "USBCD"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mediaBootTargetCandidates(tc.requested, tc.allowed, tc.manufacturer)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v want %#v", got, tc.want)
			}
		})
	}
}

func TestIsSupermicroManufacturer(t *testing.T) {
	if !isSupermicroManufacturer("Supermicro") {
		t.Fatal("expected true")
	}
	if isSupermicroManufacturer("HPE") {
		t.Fatal("expected false")
	}
}

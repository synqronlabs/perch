package imapd

import (
	"reflect"
	"testing"
)

func TestExpandRange(t *testing.T) {
	cases := []struct {
		name       string
		start, end uint32
		max        uint32
		want       []uint32
	}{
		{"open", 0, 0, 5, []uint32{1, 2, 3, 4, 5}},
		{"clamped", 2, 10, 4, []uint32{2, 3, 4}},
		{"single", 3, 3, 5, []uint32{3}},
		{"empty", 4, 3, 5, nil},
		{"max", ^uint32(0), ^uint32(0), ^uint32(0), []uint32{^uint32(0)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandRange(tc.start, tc.end, tc.max); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("expandRange(%d, %d, %d) = %v, want %v", tc.start, tc.end, tc.max, got, tc.want)
			}
		})
	}
}

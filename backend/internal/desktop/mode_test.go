package desktop

import (
	"slices"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		defaultEnabled bool
		wantEnabled    bool
		wantArgs       []string
		wantErr        bool
	}{
		{"desktop build default", []string{"-listen", "127.0.0.1:0"}, true, true, []string{"-listen", "127.0.0.1:0"}, false},
		{"disable", []string{"-desktop=false", "-workspace", `C:\code`}, true, false, []string{"-workspace", `C:\code`}, false},
		{"enable long", []string{"--desktop=true"}, false, true, nil, false},
		{"bare bool", []string{"-desktop"}, false, true, nil, false},
		{"last wins", []string{"-desktop=false", "--desktop=true"}, false, true, nil, false},
		{"after terminator preserved", []string{"--", "-desktop=false"}, true, true, []string{"--", "-desktop=false"}, false},
		{"invalid", []string{"-desktop=window"}, true, true, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.args)
			got, err := ParseMode(tc.args, tc.defaultEnabled)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got.Enabled != tc.wantEnabled || !slices.Equal(got.Args, tc.wantArgs) {
				t.Fatalf("mode = %#v, want enabled=%v args=%v", got, tc.wantEnabled, tc.wantArgs)
			}
			if !slices.Equal(tc.args, original) {
				t.Fatal("ParseMode mutated its input")
			}
		})
	}
}

package scenario

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden transcripts")

func TestGoldenTranscripts(t *testing.T) {
	ex := Examples()
	if len(ex) < 4 {
		t.Fatal("examples not embedded")
	}
	for _, name := range ExampleNames() {
		src := ex[name]
		t.Run(name, func(t *testing.T) {
			got, err := Run(src)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./scenario -update)", err)
			}
			if got != string(want) {
				t.Fatalf("transcript differs from %s:\n%s", golden, got)
			}
		})
	}
}

func TestRunIsDeterministic(t *testing.T) {
	src := Examples()["energia"]
	a, _ := Run(src)
	b, _ := Run(src)
	if a != b {
		t.Fatal("transcripts differ between runs")
	}
}

func TestScriptErrors(t *testing.T) {
	for _, s := range []string{"rec x", "clock nope", "clock 2026-11-27T10:00\nfly", "clock 2026-11-27T10:00\napprove ghost"} {
		if _, err := Run(s); err == nil {
			t.Errorf("%q should fail", s)
		}
	}
}

func TestParseMoney(t *testing.T) {
	for s, want := range map[string]int64{"39.90": 3990, "39.9": 3990, "39": 3900, "0.05": 5} {
		m, err := ParseMoney(s)
		if err != nil || int64(m) != want {
			t.Errorf("%s = %d %v", s, m, err)
		}
	}
	for _, s := range []string{"", "1.234", "-1", "a"} {
		if _, err := ParseMoney(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

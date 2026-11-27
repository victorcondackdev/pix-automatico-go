package scenario

import (
	"embed"
	"path"
	"sort"
	"strings"
)

//go:embed examples/*.txt
var examples embed.FS

// Examples returns the bundled example scripts by name (streaming, energia,
// academia, jornada1).
func Examples() map[string]string {
	entries, _ := examples.ReadDir("examples")
	out := map[string]string{}
	for _, e := range entries {
		b, _ := examples.ReadFile(path.Join("examples", e.Name()))
		out[strings.TrimSuffix(e.Name(), ".txt")] = string(b)
	}
	return out
}

// ExampleNames returns the example names in order.
func ExampleNames() []string {
	var names []string
	for n := range Examples() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

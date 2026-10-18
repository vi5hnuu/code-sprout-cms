package main

import (
	"fmt"
	"strings"

	"code-sprout-cms/internal/markdown"
)

// checkBodies runs every sample body through the same analyser that gates publishing, so
// content mistakes surface without a database (and before a seed run stops half-way).
func checkBodies() error {
	paths := make([]string, 0, len(problemCatalog)+len(pageCatalog))
	for _, p := range problemCatalog {
		paths = append(paths, "problems/"+p.slug+".md")
	}
	for _, p := range pageCatalog {
		paths = append(paths, strings.ToLower(string(p.typ))+"/"+p.slug+".md")
	}
	failed := 0
	for _, path := range paths {
		md, err := body(path)
		if err != nil {
			return err
		}
		for _, is := range markdown.Analyze([]byte(md)).Issues {
			fmt.Printf("%s:%d: %s (%s)\n", path, is.Line, is.Message, is.Code)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d issue(s) in sample content", failed)
	}
	fmt.Printf("%d bodies OK\n", len(paths))
	return nil
}

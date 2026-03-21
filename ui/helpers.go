package ui

import (
	"fmt"
	"strings"
)

type Crumb struct {
	Name   string
	Path   string
	IsLast bool
}

func buildCrumbs(prefix string) []Crumb {
	prefix = strings.TrimSuffix(prefix, "/")
	parts := strings.Split(prefix, "/")

	var crumbs []Crumb
	for i, part := range parts {
		path := strings.Join(parts[:i+1], "/") + "/"
		crumbs = append(crumbs, Crumb{
			Name:   part,
			Path:   path,
			IsLast: i == len(parts)-1,
		})
	}
	return crumbs
}

func displayName(key, prefix string) string {
	name := strings.TrimPrefix(key, prefix)
	name = strings.TrimSuffix(name, "/")
	return name
}

func formatSize(size int64) string {
	if size == 0 {
		return "0 B"
	}

	units := []string{"B", "KB", "MB", "GB", "TB"}
	fSize := float64(size)
	unitIdx := 0

	for fSize >= 1024 && unitIdx < len(units)-1 {
		fSize /= 1024
		unitIdx++
	}

	if unitIdx == 0 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", fSize, units[unitIdx])
}

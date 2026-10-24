// Package pathx contains the path helpers shared by every part of reroot.
//
// All path comparisons in reroot go through this package so that platform
// specific behaviour (separators, case sensitivity) lives in one place.
package pathx

import (
	"os"
	"path/filepath"
	"strings"
)

// Within reports whether p is root itself or lies underneath root.
// Both paths are expected to be absolute and clean.
func Within(p, root string) bool {
	if p == root {
		return true
	}
	if root == string(filepath.Separator) {
		return strings.HasPrefix(p, root)
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// Rebase rewrites p from the from-prefix to the to-prefix. It returns false
// when p is not within from.
func Rebase(p, from, to string) (string, bool) {
	if !Within(p, from) {
		return p, false
	}
	return to + p[len(from):], true
}

// Abs expands a leading "~" and resolves p against base when it is relative.
func Abs(p, base string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}
			base = wd
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), nil
}

// Rel returns p relative to root, or p itself when that is not possible.
func Rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(r, "..") {
		return p
	}
	return r
}

// NearestExisting returns p or its closest ancestor that exists on disk.
func NearestExisting(p string) string {
	for {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

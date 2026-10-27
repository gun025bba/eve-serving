package handlers

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticFiles serves the UI directory without directory listings and without
// following symlinks: a directory is served only via its index.html, and any
// path component that is a symlink (or anything but a regular file / dir) is
// answered 404.
func staticFiles(root string) http.Handler {
	fs := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		cur := root
		var st os.FileInfo
		for _, part := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
			if part == "" {
				continue
			}
			cur = filepath.Join(cur, part)
			var err error
			if st, err = os.Lstat(cur); err != nil || st.Mode()&os.ModeSymlink != 0 {
				http.NotFound(w, r)
				return
			}
		}
		if st == nil || st.IsDir() {
			idx, err := os.Lstat(filepath.Join(cur, "index.html"))
			if err != nil || !idx.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
		} else if !st.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

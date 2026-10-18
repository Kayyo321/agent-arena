package arena

import "path/filepath"

// pinnableExecutable resolves symlinks in the running binary's path. The update
// flow swaps a bin/arena symlink between versioned store directories
// (selfupdate.go), so a path that still goes through the link would start the new
// version in children that an older process spawns later. Falls back to the given
// path when resolution fails, for example when the file was removed.
func pinnableExecutable(exe string) string {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe
	}
	return resolved
}

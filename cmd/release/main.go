// release builds local standalone archives. It never publishes or uploads them.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	version := flag.String("version", "0.1.0-dev", "release version")
	flag.Parse()
	if strings.ContainsAny(*version, "/\\ \t\n") {
		panic("invalid version")
	}
	must(os.MkdirAll("dist", 0755))
	notices := licenses()
	sums := strings.Builder{}
	checksum := func(archive string) {
		b, err := os.ReadFile(archive)
		must(err)
		hash := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(hash[:]), filepath.Base(archive))
		fmt.Println(archive)
	}
	for _, platform := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64"} {
		parts := strings.Split(platform, "/")
		name := "arena_" + *version + "_" + parts[0] + "_" + parts[1]
		dir, err := os.MkdirTemp("dist", ".arena-build-")
		must(err)
		defer os.RemoveAll(dir)
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/Kayyo321/agent-arena/internal/arena.Version="+*version, "-o", filepath.Join(dir, "arena"), "./cmd/arena")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+parts[0], "GOARCH="+parts[1])
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		must(cmd.Run())
		must(copyPaths(dir, []string{"LICENSE", "README.md", "docs", "schemas", "providers", "examples", ".agents/skills"}))
		for path, b := range notices {
			target := filepath.Join(dir, "licenses", path)
			must(os.MkdirAll(filepath.Dir(target), 0755))
			must(os.WriteFile(target, b, 0644))
		}
		archive := filepath.Join("dist", name+".tar.gz")
		must(pack(dir, archive))
		checksum(archive)
	}
	source, err := os.MkdirTemp("dist", ".arena-source-")
	must(err)
	defer os.RemoveAll(source)
	must(copyPaths(source, []string{"LICENSE", "README.md", "AGENTS.md", "Makefile", "go.mod", "go.sum", "assets.go", "cmd", "internal", "docs", "schemas", "providers", "examples", ".agents", ".claude", ".github", ".gitignore"}))
	archive := filepath.Join("dist", "arena_"+*version+"_source.tar.gz")
	must(pack(source, archive))
	checksum(archive)
	must(os.WriteFile("dist/SHA256SUMS", []byte(sums.String()), 0644))
}

func copyPaths(target string, roots []string) error {
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Name() == ".DS_Store" {
				return nil
			}
			if d.IsDir() && path == filepath.Join(".claude", "worktrees") {
				return filepath.SkipDir
			}
			to := filepath.Join(target, path)
			if d.IsDir() {
				return os.MkdirAll(to, 0755)
			}
			if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				return os.Symlink(link, to)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(to, b, 0644)
		}); err != nil {
			return err
		}
	}
	return nil
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func pack(dir, target string) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name, err = filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		h.Uid = 0
		h.Gid = 0
		h.Uname = ""
		h.Gname = ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if link != "" {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
	if e := tw.Close(); err == nil {
		err = e
	}
	if e := gz.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}
func licenses() map[string][]byte {
	download := exec.Command("go", "mod", "download", "all")
	download.Stderr = os.Stderr
	must(download.Run())
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	b, err := cmd.Output()
	must(err)
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	out := map[string][]byte{}
	var index strings.Builder
	for {
		var m struct {
			Path, Version, Dir string
			Main               bool
		}
		if err := decoder.Decode(&m); err == io.EOF {
			break
		} else {
			must(err)
		}
		if m.Main {
			continue
		}
		fmt.Fprintf(&index, "%s %s\n", m.Path, m.Version)
		entries, err := os.ReadDir(m.Dir)
		must(err)
		for _, entry := range entries {
			upper := strings.ToUpper(entry.Name())
			if entry.IsDir() || !(strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || upper == "NOTICE") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(m.Dir, entry.Name()))
			must(err)
			out[filepath.Join(strings.ReplaceAll(m.Path, "/", "_"), entry.Name())] = data
		}
	}
	out["MODULES.txt"] = []byte(index.String())
	return out
}

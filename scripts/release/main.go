// Build a deterministic host-platform distribution without archiving secrets.
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	output := flag.String("output", ".cache/release", "output directory")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	epoch, err := strconv.ParseInt(os.Getenv("SOURCE_DATE_EPOCH"), 10, 64)
	if err != nil || epoch < 0 {
		return fmt.Errorf("SOURCE_DATE_EPOCH must be a nonnegative Unix timestamp")
	}
	stamp := time.Unix(epoch, 0).UTC()
	info, err := exec.Command("./bin/mass", "build-info").Output()
	if err != nil {
		return err
	}
	var build map[string]any
	if err = json.Unmarshal(info, &build); err != nil {
		return err
	}
	if build["platform"] != runtime.GOOS+"/"+runtime.GOARCH {
		return fmt.Errorf("release currently supports the build host platform only")
	}
	files := []string{"bin/mass", "configs/mass.example.yaml", "README.md", "docs/architecture.md", "docs/design-guide.md", "docs/technical-design.md", "docs/engineering.md", "docs/evaluation-design.md"}
	// Include documentation images referenced by the four existing documents.
	err = filepath.WalkDir("docs/images", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	hashes := map[string]string{}
	contents := map[string][]byte{"build-info.json": info}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := path
		if path == "configs/mass.example.yaml" {
			name = "mass.yaml"
		}
		contents[name] = data
	}
	names := make([]string, 0, len(contents))
	for name, data := range contents {
		names = append(names, name)
		hash := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(hash[:])
	}
	sort.Strings(names)
	manifest, err := json.MarshalIndent(map[string]any{"format": 1, "build": build, "source_date_epoch": epoch, "sha256": hashes}, "", "  ")
	if err != nil {
		return err
	}
	contents["manifest.json"] = append(manifest, '\n')
	names = append(names, "manifest.json")
	sort.Strings(names)
	if err = os.MkdirAll(*output, 0755); err != nil {
		return err
	}
	archive := filepath.Join(*output, "mass-"+runtime.GOOS+"-"+runtime.GOARCH+".tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, hash))
	tw := tar.NewWriter(gz)
	for _, name := range names {
		mode := int64(0644)
		if name == "bin/mass" {
			mode = 0755
		}
		data := contents[name]
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: stamp, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err = tw.Write(data); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	checksum := hex.EncodeToString(hash.Sum(nil)) + "  " + filepath.Base(archive) + "\n"
	if err = os.WriteFile(archive+".sha256", []byte(checksum), 0644); err != nil {
		return err
	}
	fmt.Println(strings.TrimSpace(checksum))
	return nil
}

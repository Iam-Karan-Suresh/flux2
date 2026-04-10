/*
Copyright 2025 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"io/fs"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type writeToMemoryFS struct {
	fs.FS

	writtenFiles map[string][]byte
}

func (m *writeToMemoryFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	m.writtenFiles[name] = data
	return nil
}

type writtenFile struct {
	file       string
	goldenFile string
}

func TestFileSystemMigrator(t *testing.T) {
	for _, tt := range []struct {
		name         string
		path         string
		outputGolden string
		writtenFiles []writtenFile
		err          string
	}{
		{
			name: "errors out for single file that is a symlink",
			path: "testdata/migrate/file-system/single-file-link.yaml",
			err:  "file testdata/migrate/file-system/single-file-link.yaml is irregular",
		},
		{
			name: "errors out for single file with wrong extension",
			path: "testdata/migrate/file-system/single-file-wrong-ext.json",
			err:  "file testdata/migrate/file-system/single-file-wrong-ext.json does not match the specified extensions: .yaml, .yml",
		},
		{
			name:         "migrate single file",
			path:         "testdata/migrate/file-system/single-file.yaml",
			outputGolden: "testdata/migrate/file-system/single-file.yaml.output.golden",
			writtenFiles: []writtenFile{
				{
					file:       "testdata/migrate/file-system/single-file.yaml",
					goldenFile: "testdata/migrate/file-system/single-file.yaml.golden",
				},
			},
		},
		{
			name:         "migrate files in directory",
			path:         "testdata/migrate/file-system/dir",
			outputGolden: "testdata/migrate/file-system/dir.output.golden",
			writtenFiles: []writtenFile{
				{
					file:       "testdata/migrate/file-system/dir/some-dir/another-file.yaml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-dir/another-file.yaml",
				},
				{
					file:       "testdata/migrate/file-system/dir/some-dir/another-file.yml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-dir/another-file.yml",
				},
				{
					file:       "testdata/migrate/file-system/dir/some-file.yaml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-file.yaml",
				},
				{
					file:       "testdata/migrate/file-system/dir/some-file.yml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-file.yml",
				},
			},
		},
		// --- new test cases for the kind-after-apiVersion bug fix ---
		{
			// Regression: old code missed kind: when metadata: appeared between
			// apiVersion: and kind:. New forward-window scan handles this.
			name:         "migrate single file with kind after metadata",
			path:         "testdata/migrate/file-system/single-file-kind-after-metadata.yaml",
			outputGolden: "testdata/migrate/file-system/single-file-kind-after-metadata.yaml.output.golden",
			writtenFiles: []writtenFile{
				{
					file:       "testdata/migrate/file-system/single-file-kind-after-metadata.yaml",
					goldenFile: "testdata/migrate/file-system/single-file-kind-after-metadata.yaml.golden",
				},
			},
		},
		{
			// Regression: old code missed kind: when comments appeared between
			// apiVersion: and kind:.
			name:         "migrate single file with kind after comments",
			path:         "testdata/migrate/file-system/single-file-kind-after-comments.yaml",
			outputGolden: "testdata/migrate/file-system/single-file-kind-after-comments.yaml.output.golden",
			writtenFiles: []writtenFile{
				{
					file:       "testdata/migrate/file-system/single-file-kind-after-comments.yaml",
					goldenFile: "testdata/migrate/file-system/single-file-kind-after-comments.yaml.golden",
				},
			},
		},
		{
			// Correctness: already up-to-date resources must not be written or reported.
			name:         "no migration needed for already up-to-date file",
			path:         "testdata/migrate/file-system/single-file-no-migration.yaml",
			outputGolden: "testdata/migrate/file-system/single-file-no-migration.yaml.output.golden",
			writtenFiles: []writtenFile{},
		},
		{
			// Boundary: kind: appears beyond kindSearchWindow lines — must be
			// safely ignored, no panic, no false positive.
			name:         "kind beyond search window is safely ignored",
			path:         "testdata/migrate/file-system/single-file-kind-beyond-window.yaml",
			outputGolden: "testdata/migrate/file-system/single-file-kind-beyond-window.yaml.output.golden",
			writtenFiles: []writtenFile{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Store logger, replace with test logger, and restore at the end of the test.
			var testLogger bytes.Buffer
			oldLogger := logger
			logger = stderrLogger{&testLogger}
			t.Cleanup(func() { logger = oldLogger })

			// Open current working directory as root and build write-to-memory filesystem.
			pathRoot, err := os.OpenRoot(".")
			g.Expect(err).ToNot(HaveOccurred())
			t.Cleanup(func() { pathRoot.Close() })
			fileSystem := &writeToMemoryFS{
				FS:           pathRoot.FS(),
				writtenFiles: make(map[string][]byte),
			}

			// Prepare other inputs.
			const yes = true
			const dryRun = false
			extensions := []string{".yaml", ".yml"}
			latestVersions := map[schema.GroupKind]string{
				{Group: "image.toolkit.fluxcd.io", Kind: "ImageRepository"}:       "v1",
				{Group: "image.toolkit.fluxcd.io", Kind: "ImagePolicy"}:           "v1",
				{Group: "image.toolkit.fluxcd.io", Kind: "ImageUpdateAutomation"}: "v1",
			}

			// Run migration.
			err = NewFileSystemMigrator(fileSystem, yes, dryRun, tt.path, extensions, latestVersions).Run()
			if tt.err != "" {
				g.Expect(err).To(HaveOccurred())
				g.Expect(err.Error()).To(Equal(tt.err))
				return
			}
			g.Expect(err).ToNot(HaveOccurred())

			// Assert logger output.
			b, err := os.ReadFile(tt.outputGolden)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(string(b)).To(Equal(testLogger.String()),
				"logger output does not match golden file %s", tt.outputGolden)

			// Assert which files were written.
			writtenFiles := make([]string, 0, len(fileSystem.writtenFiles))
			for name := range fileSystem.writtenFiles {
				writtenFiles = append(writtenFiles, name)
			}
			expectedWrittenFiles := make([]string, 0, len(tt.writtenFiles))
			for _, wf := range tt.writtenFiles {
				expectedWrittenFiles = append(expectedWrittenFiles, wf.file)
			}
			g.Expect(writtenFiles).To(ConsistOf(expectedWrittenFiles))

			// Assert contents of written files.
			for _, wf := range tt.writtenFiles {
				b, err := os.ReadFile(wf.goldenFile)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(string(fileSystem.writtenFiles[wf.file])).To(Equal(string(b)),
					"file %s does not match golden file %s", wf.file, wf.goldenFile)
			}
		})
	}
}

// BenchmarkDetectFileUpgrades compares the old (line+1 only) and new
// (forward-window) implementations under realistic file shapes.
// Run with: go test -bench=BenchmarkDetectFileUpgrades -benchmem -count=5 ./cmd/flux/
func BenchmarkDetectFileUpgrades(b *testing.B) {
    // Build a minimal migrator — no real filesystem needed for line scanning
    latestVersions := map[schema.GroupKind]string{
        {Group: "image.toolkit.fluxcd.io", Kind: "ImageRepository"}: "v1",
    }
    migrator := &FileSystemMigrator{
        latestVersions: latestVersions,
    }

    standardFile := `apiVersion: image.toolkit.fluxcd.io/v1beta1
kind: ImageRepository
metadata:
  name: podinfo
  namespace: flux-system`

    offsetFile := `apiVersion: image.toolkit.fluxcd.io/v1beta1
metadata:
  name: podinfo
  namespace: flux-system
kind: ImageRepository`

    var sb strings.Builder
    for i := 0; i < 100; i++ {
        sb.WriteString("apiVersion: image.toolkit.fluxcd.io/v1beta1\n")
        sb.WriteString("metadata:\n")
        sb.WriteString("  name: podinfo\n")
        sb.WriteString("kind: ImageRepository\n\n")
    }
    largeFile := sb.String()

    b.Run("old_standard_order", func(b *testing.B) {
        lines := strings.Split(standardFile, "\n")
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            migrator.detectFileUpgradesOld(lines)
        }
    })

    b.Run("new_standard_order", func(b *testing.B) {
        lines := strings.Split(standardFile, "\n")
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            migrator.detectFileUpgradesNew(lines)
        }
    })

    b.Run("old_kind_at_offset3_bug", func(b *testing.B) {
        lines := strings.Split(offsetFile, "\n")
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            migrator.detectFileUpgradesOld(lines)
        }
    })

    b.Run("new_kind_at_offset3_bug", func(b *testing.B) {
        lines := strings.Split(offsetFile, "\n")
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            migrator.detectFileUpgradesNew(lines)
        }
    })

    b.Run("old_100_resources", func(b *testing.B) {
        lines := strings.Split(largeFile, "\n")
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            migrator.detectFileUpgradesOld(lines)
        }
    })

    b.Run("new_100_resources", func(b *testing.B) {
        lines := strings.Split(largeFile, "\n")
        b.ResetTimer()
        for i := 0; i < b.N; i++ {
            migrator.detectFileUpgradesNew(lines)
        }
    })
}
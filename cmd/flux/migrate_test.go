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
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"

	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/fluxcd/flux2/v2/internal/utils"
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
			name:         "migrate single file that is a symlink",
			path:         "testdata/migrate/file-system/single-file-link.yaml",
			outputGolden: "testdata/migrate/file-system/single-file-link.yaml.output.golden",
			writtenFiles: []writtenFile{
				{
					file:       "testdata/migrate/file-system/single-file-link.yaml",
					goldenFile: "testdata/migrate/file-system/single-file.yaml.golden",
				},
			},
		},
		{
			name: "errors out for single file with wrong extension",
			path: "testdata/migrate/file-system/single-file-wrong-ext.json",
			err:  "file testdata/migrate/file-system/single-file-wrong-ext.json does not match the specified extensions: .yaml, .yml",
		},
		{
			name:         "migrate file with corruption prevention",
			path:         "testdata/migrate/file-system/corruption.yaml",
			outputGolden: "testdata/migrate/file-system/corruption.yaml.output.golden",
			writtenFiles: []writtenFile{
				{
					file:       "testdata/migrate/file-system/corruption.yaml",
					goldenFile: "testdata/migrate/file-system/corruption.yaml.golden",
				},
			},
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
					file:       "testdata/migrate/file-system/dir/some-dir/another-file-link.yaml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-dir/another-file.yaml",
				},
				{
					file:       "testdata/migrate/file-system/dir/some-dir/another-file.yaml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-dir/another-file.yaml",
				},
				{
					file:       "testdata/migrate/file-system/dir/some-dir/another-file.yml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-dir/another-file.yml",
				},
				{
					file:       "testdata/migrate/file-system/dir/some-file-link.yaml",
					goldenFile: "testdata/migrate/file-system/dir.golden/some-file.yaml",
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
			g.Expect(testLogger.String()).To(Equal(string(b)),
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

func TestClusterMigrator_migrateCRD_StatusUpdate(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	scheme := utils.NewScheme()

	// Prepare logger.
	var testLogger bytes.Buffer
	oldLogger := logger
	logger = stderrLogger{&testLogger}
	defer func() { logger = oldLogger }()

	tests := []struct {
		name           string
		storedVersions []string
		storageVersion string
		expectUpdate   bool
	}{
		{
			name:           "updates status when stored versions is empty (fixes panic)",
			storedVersions: []string{},
			storageVersion: "v1",
			expectUpdate:   true,
		},
		{
			name:           "updates status when multiple versions exist",
			storedVersions: []string{"v1beta1", "v1"},
			storageVersion: "v1",
			expectUpdate:   true,
		},
		{
			name:           "updates status when version is wrong",
			storedVersions: []string{"v1beta1"},
			storageVersion: "v1",
			expectUpdate:   true,
		},
		{
			name:           "does not update status when version is correct",
			storedVersions: []string{"v1"},
			storageVersion: "v1",
			expectUpdate:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crd := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test.fluxcd.io",
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "test.fluxcd.io",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Kind:     "Test",
						ListKind: "TestList",
					},
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{Name: tt.storageVersion, Storage: true, Served: true},
					},
				},
				Status: apiextensionsv1.CustomResourceDefinitionStatus{
					StoredVersions: tt.storedVersions,
				},
			}

			fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
			migrator := NewClusterMigrator(fc, nil)

			err := migrator.migrateCRD(ctx, crd.Name)
			g.Expect(err).ToNot(HaveOccurred())

			// Check status.
			updatedCRD := &apiextensionsv1.CustomResourceDefinition{}
			err = fc.Get(ctx, client.ObjectKey{Name: crd.Name}, updatedCRD)
			g.Expect(err).ToNot(HaveOccurred())

			g.Expect(updatedCRD.Status.StoredVersions).To(ConsistOf(tt.storageVersion))

			if tt.expectUpdate {
				g.Expect(testLogger.String()).To(ContainSubstring(fmt.Sprintf("%s migrated to storage version %s", crd.Name, tt.storageVersion)))
			} else {
				g.Expect(testLogger.String()).ToNot(ContainSubstring("migrated to storage version"))
			}
			testLogger.Reset()
		})
	}
}

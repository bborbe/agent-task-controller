// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	lib "github.com/bborbe/agent"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/bborbe/agent-task-controller/pkg/metrics"
)

// aliasContent wraps a frontmatter fragment in the same surrounding keys the
// spec-008 fixtures use, so a repair sees a realistic file.
func aliasContent(frontmatter string) string {
	return "---\n" + frontmatter + "\nstatus: in_progress\nassignee: claude\n---\nbody\n"
}

// newAliasScanner returns a scanner whose file operations are bound to tmpDir.
func newAliasScanner(tmpDir string, writeCount *int) *vaultScanner {
	return &vaultScanner{
		metrics: metrics.New(),
		hashes:  make(map[string]fileEntry),
		ops: fileOps{
			readFile: func(_ context.Context, p string) ([]byte, error) {
				return os.ReadFile(filepath.Join(tmpDir, p)) // #nosec G304 -- test-only path
			},
			writeFile: func(_ context.Context, relPath string, content []byte) error {
				if writeCount != nil {
					*writeCount++
				}
				return os.WriteFile(filepath.Join(tmpDir, relPath), content, 0600)
			},
		},
		autoInject: true,
	}
}

// aliasValue unmarshals the extracted frontmatter and returns the
// task_identifier_previous value, or "" when absent.
func aliasValue(ctx context.Context, content []byte) string {
	fm, err := extractFrontmatter(ctx, content)
	Expect(err).NotTo(HaveOccurred())
	var fmMap map[string]interface{}
	Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
	value, _ := fmMap["task_identifier_previous"].(string)
	return value
}

var _ = Describe("task_identifier_previous alias (spec 021)", func() {
	var previousKeyLineRe = regexp.MustCompile("^task_identifier_previous:")

	It("AC1: records the replaced string identifier in the same repair write", func() {
		ctx := context.Background()
		rows := []struct {
			label       string
			relPath     string
			frontmatter string
			expected    string
		}{
			{"numeric-looking quoted", "alias-501.md", `task_identifier: "501"`, "501"},
			{"live incident shape", "alias-slug.md", "task_identifier: my-slug", "my-slug"},
		}

		for _, row := range rows {
			tmpDir, err := os.MkdirTemp("", "scanner-spec021-ac1-*")
			Expect(err).NotTo(HaveOccurred())

			Expect(
				os.WriteFile(
					filepath.Join(tmpDir, row.relPath),
					[]byte(aliasContent(row.frontmatter)),
					0600,
				),
			).To(Succeed())

			var writeCount int
			v := newAliasScanner(tmpDir, &writeCount)
			_, _, werr := v.processFile(ctx, row.relPath)
			Expect(werr).To(BeFalse(), row.label)
			Expect(writeCount).To(Equal(1), row.label)

			finalBytes, err := os.ReadFile(
				filepath.Join(tmpDir, row.relPath),
			) // #nosec G304 -- test-only path
			Expect(err).NotTo(HaveOccurred(), row.label)

			Expect(
				countLinesMatching(finalBytes, previousKeyLineRe),
			).To(Equal(1), row.label)
			Expect(aliasValue(ctx, finalBytes)).To(Equal(row.expected), row.label)

			fm, err := extractFrontmatter(ctx, finalBytes)
			Expect(err).NotTo(HaveOccurred(), row.label)
			var fmMap map[string]interface{}
			Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed(), row.label)
			idStr, isString := fmMap["task_identifier"].(string)
			Expect(isString).To(BeTrue(), row.label)
			_, parseErr := uuid.Parse(idStr)
			Expect(parseErr).NotTo(HaveOccurred(), row.label)
			Expect(repairConverges(ctx, finalBytes, idStr)).To(BeTrue(), row.label)

			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		}
	})

	It("AC3: shapes that were never live identifiers record no alias", func() {
		ctx := context.Background()
		// Rows 1-12 and 14-16 of the spec-008 fixture table. Row 13 (a quoted
		// non-UUID string) is deliberately excluded: it is the alias shape and
		// is covered by AC1.
		fixtures := []struct {
			label       string
			relPath     string
			frontmatter string
		}{
			{"row 1 - unquoted int", "alias-ac3-01.md", "task_identifier: 501"},
			{"row 2 - bool", "alias-ac3-02.md", "task_identifier: true"},
			{"row 3 - null", "alias-ac3-03.md", "task_identifier: null"},
			{"row 4 - float", "alias-ac3-04.md", "task_identifier: 1.5"},
			{"row 5 - empty double-quoted string", "alias-ac3-05.md", `task_identifier: ""`},
			{"row 6 - empty single-quoted string", "alias-ac3-06.md", `task_identifier: ''`},
			{"row 7 - whitespace-only string", "alias-ac3-07.md", `task_identifier: "   "`},
			{"row 8 - flow sequence", "alias-ac3-08.md", "task_identifier: [a, b]"},
			{"row 9 - flow mapping", "alias-ac3-09.md", "task_identifier: {a: b}"},
			{
				"row 10 - block-style sequence",
				"alias-ac3-10.md",
				"task_identifier:\n  - a\n  - b",
			},
			{"row 11 - block-style mapping", "alias-ac3-11.md", "task_identifier:\n  a: b"},
			{"row 12 - block scalar", "alias-ac3-12.md", "task_identifier: |\n  abc"},
			{"row 14 - double-quoted key", "alias-ac3-14.md", `"task_identifier": 501`},
			{"row 15 - space before colon", "alias-ac3-15.md", "task_identifier : 501"},
			{"row 16 - single-quoted key", "alias-ac3-16.md", `'task_identifier': 501`},
			{
				"key absent",
				"alias-ac3-17.md",
				"",
			},
		}

		tmpDir, err := os.MkdirTemp("", "scanner-spec021-ac3-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		captured := captureGlogWarnings(func() {
			for _, fx := range fixtures {
				fixture := aliasContent(fx.frontmatter)
				if fx.frontmatter == "" {
					fixture = "---\nstatus: in_progress\nassignee: claude\n---\nbody\n"
				}
				Expect(
					os.WriteFile(filepath.Join(tmpDir, fx.relPath), []byte(fixture), 0600),
				).To(Succeed())

				// A fresh scanner per row keeps each row's repair independent.
				v := newAliasScanner(tmpDir, nil)
				_, _, werr := v.processFile(ctx, fx.relPath)
				Expect(werr).To(BeFalse(), fx.label)

				finalBytes, err := os.ReadFile(
					filepath.Join(tmpDir, fx.relPath),
				) // #nosec G304 -- test-only path
				Expect(err).NotTo(HaveOccurred(), fx.label)
				Expect(
					countLinesMatching(finalBytes, previousKeyLineRe),
				).To(Equal(0), fx.label)
			}
		})

		Expect(
			strings.Count(captured, "replacing invalid task_identifier of type"),
		).To(Equal(11), "eleven non-string shapes must log the invalid-type line")
	})

	It("AC4: the duplicate branch writes no alias", func() {
		ctx := context.Background()
		const sharedUUID = "77777777-7777-4777-8777-777777777777"

		tmpDir, err := os.MkdirTemp("", "scanner-spec021-ac4-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		fixture := aliasContent("task_identifier: " + sharedUUID)
		Expect(
			os.WriteFile(filepath.Join(tmpDir, "dup-second.md"), []byte(fixture), 0600),
		).To(Succeed())

		v := newAliasScanner(tmpDir, nil)
		v.hashes["dup-first.md"] = fileEntry{
			taskIdentifier: lib.TaskIdentifier(sharedUUID),
		}

		captured := captureGlogWarnings(func() {
			_, _, werr := v.processFile(ctx, "dup-second.md")
			Expect(werr).To(BeFalse())
		})

		finalBytes, err := os.ReadFile(
			filepath.Join(tmpDir, "dup-second.md"),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		Expect(countLinesMatching(finalBytes, previousKeyLineRe)).To(Equal(0))
		Expect(captured).To(ContainSubstring("replacing duplicate task_identifier"))
	})

	It("AC6: a second repair replaces the recorded predecessor instead of appending", func() {
		ctx := context.Background()
		tmpDir, err := os.MkdirTemp("", "scanner-spec021-ac6-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(os.RemoveAll(tmpDir)).To(Succeed()) }()

		relPath := "alias-ac6.md"
		Expect(
			os.WriteFile(
				filepath.Join(tmpDir, relPath),
				[]byte(aliasContent("task_identifier: slug-a")),
				0600,
			),
		).To(Succeed())

		var writeCount int
		v := newAliasScanner(tmpDir, &writeCount)
		_, _, werr := v.processFile(ctx, relPath)
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(1))

		// Change only the current key line, leaving the recorded predecessor in
		// place: a rewrite of the whole file would pass without exercising
		// replace-not-append.
		first, err := os.ReadFile(filepath.Join(tmpDir, relPath)) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		currentKeyLineRe := regexp.MustCompile("(?m)^task_identifier: .*$")
		Expect(currentKeyLineRe.Match(first)).To(BeTrue())
		second := currentKeyLineRe.ReplaceAll(first, []byte("task_identifier: slug-b"))
		Expect(os.WriteFile(filepath.Join(tmpDir, relPath), second, 0600)).To(Succeed())

		_, _, werr = v.processFile(ctx, relPath)
		Expect(werr).To(BeFalse())
		Expect(writeCount).To(Equal(2))

		finalBytes, err := os.ReadFile(
			filepath.Join(tmpDir, relPath),
		) // #nosec G304 -- test-only path
		Expect(err).NotTo(HaveOccurred())
		Expect(countLinesMatching(finalBytes, previousKeyLineRe)).To(Equal(1))
		Expect(aliasValue(ctx, finalBytes)).To(Equal("slug-b"))

		fm, err := extractFrontmatter(ctx, finalBytes)
		Expect(err).NotTo(HaveOccurred())
		var fmMap map[string]interface{}
		Expect(yaml.Unmarshal([]byte(fm), &fmMap)).To(Succeed())
		idStr, isString := fmMap["task_identifier"].(string)
		Expect(isString).To(BeTrue())
		_, parseErr := uuid.Parse(idStr)
		Expect(parseErr).NotTo(HaveOccurred())
	})
})

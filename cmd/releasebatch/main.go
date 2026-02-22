package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var tagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)-(\d+)$`)

type versionTag struct {
	Raw   string
	Major int
	Minor int
	Patch int
	Batch int
}

func parseVersionTag(raw string) (versionTag, bool) {
	matches := tagPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(matches) != 5 {
		return versionTag{}, false
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		return versionTag{}, false
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		return versionTag{}, false
	}
	patch, err := strconv.Atoi(matches[3])
	if err != nil {
		return versionTag{}, false
	}
	batch, err := strconv.Atoi(matches[4])
	if err != nil {
		return versionTag{}, false
	}
	return versionTag{
		Raw:   raw,
		Major: major,
		Minor: minor,
		Patch: patch,
		Batch: batch,
	}, true
}

func (v versionTag) less(other versionTag) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	if v.Patch != other.Patch {
		return v.Patch < other.Patch
	}
	return v.Batch < other.Batch
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func ensureCleanWorkingTree() error {
	out, err := run("git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return errors.New("working tree is not clean")
	}
	return nil
}

func versionTags() ([]versionTag, error) {
	out, err := run("git", "tag", "--list", "v*")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	tags := make([]versionTag, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parsed, ok := parseVersionTag(line)
		if ok {
			tags = append(tags, parsed)
		}
	}
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].less(tags[j])
	})
	if len(tags) == 0 {
		return nil, errors.New("no version tags matching v<major>.<minor>.<patch>-<batch>")
	}
	return tags, nil
}

func commitsInRange(rangeSpec string) ([]string, error) {
	out, err := run("git", "log", "--pretty=%H %s", rangeSpec)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result, nil
}

func buildNotes(commits []string) string {
	var b strings.Builder
	b.WriteString("## Changelog\n")
	for _, c := range commits {
		b.WriteString("* ")
		b.WriteString(c)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

func createMode(targetBranch string, hotfix bool, dryRun bool) error {
	if err := ensureCleanWorkingTree(); err != nil {
		return err
	}
	if _, err := run("git", "fetch", "origin", targetBranch, "--quiet"); err != nil {
		return err
	}
	if _, err := run("git", "fetch", "--tags", "origin", "--quiet"); err != nil {
		return err
	}

	tags, err := versionTags()
	if err != nil {
		return err
	}
	latest := tags[len(tags)-1]

	next := latest
	if hotfix {
		next.Batch++
	} else {
		next.Patch++
		next.Batch = 0
	}
	next.Raw = fmt.Sprintf("v%d.%d.%d-%d", next.Major, next.Minor, next.Patch, next.Batch)

	rangeSpec := fmt.Sprintf("%s..origin/%s", latest.Raw, targetBranch)
	commits, err := commitsInRange(rangeSpec)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits found in range %s", rangeSpec)
	}
	notes := buildNotes(commits)

	fmt.Printf("latest tag : %s\n", latest.Raw)
	fmt.Printf("next tag   : %s\n", next.Raw)
	fmt.Printf("target     : origin/%s\n", targetBranch)
	fmt.Printf("commits    : %d\n", len(commits))

	if dryRun {
		fmt.Printf("\n--- release notes preview ---\n%s", notes)
		return nil
	}

	if _, err := run("git", "tag", "-a", next.Raw, "origin/"+targetBranch, "-m", next.Raw); err != nil {
		return err
	}
	if _, err := run("git", "push", "origin", next.Raw); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp("", "release-notes-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(notes); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if _, err := run("gh", "release", "create", next.Raw, "--title", next.Raw, "--target", targetBranch, "--notes-file", tmpFile.Name()); err != nil {
		return err
	}
	fmt.Printf("release published: %s\n", next.Raw)
	return nil
}

func notesMode(tag string, outputPath string, editRelease bool) error {
	if tag == "" {
		return errors.New("notes mode requires --tag")
	}
	if _, err := run("git", "fetch", "--tags", "origin", "--quiet"); err != nil {
		return err
	}

	tags, err := versionTags()
	if err != nil {
		return err
	}

	currentIndex := -1
	for i, t := range tags {
		if t.Raw == tag {
			currentIndex = i
			break
		}
	}
	if currentIndex == -1 {
		return fmt.Errorf("tag %s not found in version tag set", tag)
	}

	var rangeSpec string
	if currentIndex == 0 {
		rangeSpec = tag
	} else {
		rangeSpec = fmt.Sprintf("%s..%s", tags[currentIndex-1].Raw, tag)
	}

	commits, err := commitsInRange(rangeSpec)
	if err != nil {
		return err
	}
	notes := buildNotes(commits)

	if outputPath == "" {
		fmt.Print(notes)
	} else {
		if err := os.WriteFile(outputPath, []byte(notes), 0o644); err != nil {
			return err
		}
	}

	if editRelease {
		notesArg := outputPath
		if notesArg == "" {
			tmpFile, err := os.CreateTemp("", "release-notes-*.md")
			if err != nil {
				return err
			}
			defer os.Remove(tmpFile.Name())
			if _, err := tmpFile.WriteString(notes); err != nil {
				return err
			}
			if err := tmpFile.Close(); err != nil {
				return err
			}
			notesArg = tmpFile.Name()
		}
		if _, err := run("gh", "release", "edit", tag, "--notes-file", notesArg); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	mode := flag.String("mode", "create", "Mode: create|notes")
	target := flag.String("target", "main", "Target branch for create mode")
	hotfix := flag.Bool("hotfix", false, "Create hotfix batch tag (same patch, +batch)")
	dryRun := flag.Bool("dry-run", false, "Preview only (create mode)")
	tag := flag.String("tag", "", "Tag for notes mode (example: v6.8.24-0)")
	out := flag.String("out", "", "Output file path for notes mode (default stdout)")
	editRelease := flag.Bool("edit-release", false, "Edit existing GitHub release notes in notes mode")
	flag.Parse()

	var err error
	switch *mode {
	case "create":
		err = createMode(*target, *hotfix, *dryRun)
	case "notes":
		err = notesMode(*tag, *out, *editRelease)
	default:
		err = fmt.Errorf("unknown mode: %s", *mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

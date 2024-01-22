package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"flag"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
)

var flagStatus = flag.String("status", "all", "Filter releases by status. Valid values [all|latest|outdated]")
var flagUpdateRepos = flag.Bool("update-repos", true, "Whether or not to update helm repos.")

type HelmChartInfo struct {
	Name      string `yaml:"name"`
	Version   string `yaml:"version"`
	Installed *bool  `yaml:"installed"`
}

type Release struct {
	Name      string `yaml:"name"`
	Chart     string `yaml:"chart"`
	Version   string `yaml:"version"`
	Installed *bool  `yaml:"installed"`
}

type Helmfile struct {
	Releases []Release `yaml:"releases"`
}

func NewHelmfile(path string) (*Helmfile, error) {
	fpath := filepath.Join(".", path)
	content, err := os.ReadFile(fpath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", fpath, err)
	}

	var h Helmfile
	err = yaml.Unmarshal(content, &h)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML data: %w", err)
	}

	return &h, nil
}

type ReleaseComparer struct {
	Current Release
	Latest  Release
}

func (rc *ReleaseComparer) Name() string {
	return rc.Current.Name
}

func (rc *ReleaseComparer) HasUpdate() bool {
	currentVersion := strings.TrimPrefix(rc.Current.Version, "v")
	latestVersion := strings.TrimPrefix(rc.Latest.Version, "v")

	if currentVersion == latestVersion {
		return false
	}

	currentSemver, err := semver.NewVersion(currentVersion)
	if err != nil {
		panic(fmt.Errorf("failed to parse current version %s %s: %w", rc.Current.Name, currentVersion, err))
	}

	latestSemver, err := semver.NewVersion(latestVersion)
	if err != nil {
		panic(fmt.Errorf("failed to parse latest version %s %s: %w", rc.Current.Name, latestVersion, err))
	}

	return currentSemver.LessThan(latestSemver)
}

type UpdateManager struct {
	Helmfile    *Helmfile
	Comparisons []*ReleaseComparer
}

func NewUpdateManager(helmfile *Helmfile) *UpdateManager {
	return &UpdateManager{
		Helmfile: helmfile,
	}
}

func (um *UpdateManager) HasUpdates() bool {
	for _, comparer := range um.Comparisons {
		if comparer.HasUpdate() {
			return true
		}
	}
	return false
}

func (um *UpdateManager) UpdateRepositories() error {
	cmd := exec.Command("helm", "repo", "update")

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("failed to wait for command: %w", err)
	}

	return nil
}

func (um *UpdateManager) GetReleaseComparer(release Release) (*ReleaseComparer, error) {
	installed := true
	if release.Installed == nil {
		release.Installed = &installed
	}

	if strings.HasPrefix(release.Chart, "/") || strings.HasPrefix(release.Chart, "./") || strings.HasPrefix(release.Chart, "../") {
		return &ReleaseComparer{
			Current: release,
			Latest:  release,
		}, nil
	}

	cmd := exec.Command("helm", "search", "repo", release.Chart, "--output", "yaml")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to search for chart %s: %w", release.Chart, err)
	}

	var chart []HelmChartInfo
	if err = yaml.Unmarshal(output, &chart); err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML data: %w", err)
	}

	if len(chart) == 0 {
		return nil, fmt.Errorf("chart %s not found", release.Chart)
	}

	latestRelease := Release{
		Name:      release.Name,
		Chart:     chart[0].Name,
		Version:   chart[0].Version,
		Installed: chart[0].Installed,
	}

	return &ReleaseComparer{
		Current: release,
		Latest:  latestRelease,
	}, nil
}

func (um *UpdateManager) CheckForUpdates() error {
	comparisons := make([]*ReleaseComparer, len(um.Helmfile.Releases))

	var wg sync.WaitGroup
	wg.Add(len(um.Helmfile.Releases))
	for i, release := range um.Helmfile.Releases {
		go func(i int, release Release) {
			defer wg.Done()

			comparer, err := um.GetReleaseComparer(release)
			if err != nil {
				panic(err)
			}

			comparisons[i] = comparer
		}(i, release)
	}
	wg.Wait()

	um.Comparisons = make([]*ReleaseComparer, 0, len(comparisons))
	um.Comparisons = append(um.Comparisons, comparisons...)

	return nil
}

func main() {
	flag.Parse()

	helmfile, err := NewHelmfile("helmfile.yaml")
	if err != nil {
		panic(err)
	}

	updateManager := NewUpdateManager(helmfile)

	if *flagUpdateRepos {
		if err = updateManager.UpdateRepositories(); err != nil {
			panic(err)
		}
		fmt.Println()
	}

	fmt.Println("Comparing release versions...")
	if err = updateManager.CheckForUpdates(); err != nil {
		panic(err)
	}

	if !updateManager.HasUpdates() {
		fmt.Println("Charts are up-to-date 🎉")
		return
	}

	// Calculate the width of the longest release name
	namePadding := 0
	versionPadding := 0
	for _, release := range updateManager.Comparisons {
		if len(release.Name()) > namePadding {
			namePadding = len(release.Name())
		}
		if len(release.Current.Version) > versionPadding {
			versionPadding = len(release.Current.Version)
		}
	}
	namePadding = namePadding + 1
	versionPadding = versionPadding + 1

	text := fmt.Sprintf(
		"%-[1]*[2]s %-[3]*[4]s  %[3]*[5]s %[3]*[6]s",
		namePadding,
		"Chart",
		versionPadding,
		"Current",
		"Latest",
		"Status",
	)
	fmt.Println()
	fmt.Println(text)

	for _, release := range updateManager.Comparisons {
		status := "✅"
		if release.HasUpdate() {
			status = "⬆️"
		}

		text := fmt.Sprintf(
			"%-[1]*[2]s %-[3]*[4]s  %[3]*[5]s     %[6]s",
			namePadding,
			release.Name(),
			versionPadding,
			release.Current.Version,
			release.Latest.Version,
			status,
		)

		switch *flagStatus {
		case "all":
			fmt.Println(text)
		case "outdated":
			if release.HasUpdate() {
				fmt.Println(text)
			}
		case "latest":
			if !release.HasUpdate() {
				fmt.Println(text)
			}
		}
	}
}

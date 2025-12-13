package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
)

// TaskRelease represents a GitHub release
type TaskRelease struct {
	TagName string `json:"tag_name"`
}

// NuGetVersionsResponse represents the response from NuGet API
type NuGetVersionsResponse struct {
	Versions []string `json:"versions"`
}

// NuGetServiceIndexResponse represents the NuGet service index
type NuGetServiceIndexResponse struct {
	Resources []NuGetResource `json:"resources"`
}

// NuGetResource represents a resource in the NuGet service index
type NuGetResource struct {
	Type string `json:"@type"`
	ID   string `json:"@id"`
}

func fetchTaskVersions() ([]string, error) {
	resp, err := http.Get("https://api.github.com/repos/go-task/task/releases?per_page=100")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var releases []TaskRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}

	var versions []string
	for _, release := range releases {
		version := strings.TrimPrefix(release.TagName, "v")
		// Only include normal releases (no nightly, preview, etc.)
		if isNormalVersion(version) {
			versions = append(versions, version)
		}
	}

	return versions, nil
}

func fetchNuGetVersions(packageId string) ([]string, error) {
	// Get service index
	resp, err := http.Get("https://api.nuget.org/v3/index.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var serviceIndex NuGetServiceIndexResponse
	if err := json.NewDecoder(resp.Body).Decode(&serviceIndex); err != nil {
		return nil, err
	}

	var packageBaseAddress string
	for _, resource := range serviceIndex.Resources {
		if resource.Type == "PackageBaseAddress/3.0.0" {
			packageBaseAddress = resource.ID
			break
		}
	}

	if packageBaseAddress == "" {
		return nil, fmt.Errorf("package base address not found")
	}

	// Get package versions
	versionsURL := fmt.Sprintf("%s%s/index.json", packageBaseAddress, strings.ToLower(packageId))
	resp, err = http.Get(versionsURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var versionsResponse NuGetVersionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&versionsResponse); err != nil {
		return nil, err
	}

	// Filter to normal versions only (no prerelease)
	var normalVersions []string
	for _, version := range versionsResponse.Versions {
		if isNormalVersion(version) && !strings.Contains(version, "-") {
			normalVersions = append(normalVersions, version)
		}
	}

	return normalVersions, nil
}

func isNormalVersion(version string) bool {
	if strings.Contains(version, "nightly") ||
		strings.Contains(version, "preview") ||
		strings.Contains(version, "alpha") ||
		strings.Contains(version, "beta") ||
		strings.Contains(version, "rc") {
		return false
	}

	// Must start with a digit
	if len(version) == 0 || version[0] < '0' || version[0] > '9' {
		return false
	}

	return true
}

func normalizeVersion(version string) string {
	// Remove v prefix
	version = strings.TrimPrefix(version, "v")

	// Split by dots
	parts := strings.Split(version, ".")

	// Ensure 3 parts
	for len(parts) < 3 {
		parts = append(parts, "0")
	}

	return strings.Join(parts[:3], ".")
}

func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(normalizeVersion(v1), ".")
	parts2 := strings.Split(normalizeVersion(v2), ".")

	for i := 0; i < 3; i++ {
		n1, _ := strconv.Atoi(parts1[i])
		n2, _ := strconv.Atoi(parts2[i])

		if n1 < n2 {
			return -1
		} else if n1 > n2 {
			return 1
		}
	}
	return 0
}

func sortVersions(versions []string) []string {
	sorted := make([]string, len(versions))
	copy(sorted, versions)

	sort.Slice(sorted, func(i, j int) bool {
		return compareVersions(sorted[i], sorted[j]) > 0
	})

	return sorted
}

func getTaskFileName(platform, arch string) string {
	if platform == "windows" {
		platform = "windows"
	}

	switch arch {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	case "arm":
		arch = "arm"
	case "386":
		arch = "386"
	}

	ext := "tar.gz"
	if platform == "windows" {
		ext = "zip"
	}

	return fmt.Sprintf("task_%s_%s.%s", platform, arch, ext)
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	os.MkdirAll(dest, 0755)

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}

		path := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.FileInfo().Mode())
		} else {
			os.MkdirAll(filepath.Dir(path), 0755)
			outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
			if err != nil {
				rc.Close()
				return err
			}

			_, err = io.Copy(outFile, rc)
			outFile.Close()
			if err != nil {
				rc.Close()
				return err
			}
		}
		rc.Close()
	}

	return nil
}

func extractTarGz(src, dest string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}

			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}

	return nil
}

func downloadTask(version, customName, outputDir, platform, arch string) error {
	// Ensure version has 'v' prefix
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	fileName := getTaskFileName(platform, arch)
	downloadUrl := fmt.Sprintf("https://github.com/go-task/task/releases/download/%s/%s", version, fileName)

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "task-download-")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	downloadPath := filepath.Join(tempDir, fileName)

	// Download
	fmt.Printf("Downloading Task %s for %s/%s...\n", version, platform, arch)
	err = downloadFile(downloadUrl, downloadPath)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}

	// Extract
	extractDir := filepath.Join(tempDir, "extracted")
	if platform == "windows" {
		err = extractZip(downloadPath, extractDir)
	} else {
		err = extractTarGz(downloadPath, extractDir)
	}

	if err != nil {
		return fmt.Errorf("failed to extract: %w", err)
	}

	// Create output directory
	err = os.MkdirAll(outputDir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Move binary to final location
	taskBinary := "task"
	if platform == "windows" {
		taskBinary = "task.exe"
	}

	srcPath := filepath.Join(extractDir, taskBinary)
	destPath := filepath.Join(outputDir, customName)
	if platform == "windows" && !strings.HasSuffix(customName, ".exe") {
		destPath += ".exe"
	}

	err = os.Rename(srcPath, destPath)
	if err != nil {
		return fmt.Errorf("failed to move binary: %w", err)
	}

	// Make executable on Unix
	if platform != "windows" {
		err = os.Chmod(destPath, 0755)
		if err != nil {
			return fmt.Errorf("failed to make executable: %w", err)
		}
	}

	fmt.Printf("Downloaded Task %s for %s/%s as %s\n", version, platform, arch, destPath)
	return nil
}

func setVersion(csprojPath, version string) error {
	content, err := os.ReadFile(csprojPath)
	if err != nil {
		return fmt.Errorf("failed to read csproj file: %w", err)
	}

	text := string(content)

	// Check if <Version> tag exists
	versionRegex := regexp.MustCompile(`<Version>.*?</Version>`)
	if versionRegex.MatchString(text) {
		// Replace existing version
		text = versionRegex.ReplaceAllString(text, fmt.Sprintf("<Version>%s</Version>", version))
	} else {
		// Add version tag after <PropertyGroup>
		propertyGroupRegex := regexp.MustCompile(`(<PropertyGroup>\s*)`)
		if propertyGroupRegex.MatchString(text) {
			text = propertyGroupRegex.ReplaceAllString(text, fmt.Sprintf("${1}\n    <Version>%s</Version>", version))
		} else {
			return fmt.Errorf("no <PropertyGroup> found in csproj file")
		}
	}

	err = os.WriteFile(csprojPath, []byte(text), 0644)
	if err != nil {
		return fmt.Errorf("failed to write csproj file: %w", err)
	}

	fmt.Printf("Set version %s in %s\n", version, csprojPath)
	return nil
}

func main() {
	app := &cli.App{
		Name:  "task-net",
		Usage: "Compare Task versions with NuGet packages and download Task releases",
		Commands: []*cli.Command{
			{
				Name:  "compare",
				Usage: "Compare Task versions with NuGet package versions",
				Action: func(c *cli.Context) error {
					if c.NArg() != 1 {
						return fmt.Errorf("usage: task-net compare <nuget-package-id>")
					}

					packageId := c.Args().First()

					taskVersions, err := fetchTaskVersions()
					if err != nil {
						return fmt.Errorf("failed to fetch Task versions: %w", err)
					}

					nugetVersions, err := fetchNuGetVersions(packageId)
					if err != nil {
						return fmt.Errorf("failed to fetch NuGet versions: %w", err)
					}

					// Create set of normalized NuGet versions
					nugetSet := make(map[string]bool)
					for _, version := range nugetVersions {
						nugetSet[normalizeVersion(version)] = true
					}

					// Find Task versions not in NuGet
					var taskOnly []string
					for _, version := range taskVersions {
						normalized := normalizeVersion(version)
						if !nugetSet[normalized] {
							taskOnly = append(taskOnly, version)
						}
					}

					// Sort and print results
					taskOnly = sortVersions(taskOnly)

					for _, version := range taskOnly {
						fmt.Println(version)
					}

					return nil
				},
			},
			{
				Name:  "download",
				Usage: "Download a specific Task release",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "version",
						Aliases:  []string{"v"},
						Usage:    "Task version to download",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "name",
						Aliases:  []string{"n"},
						Usage:    "Custom name for the binary",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "output",
						Aliases:  []string{"o"},
						Usage:    "Output directory",
						Required: true,
					},
					&cli.StringFlag{
						Name:    "platform",
						Aliases: []string{"p"},
						Usage:   "Target platform (linux, darwin, windows)",
						Value:   runtime.GOOS,
					},
					&cli.StringFlag{
						Name:    "arch",
						Aliases: []string{"a"},
						Usage:   "Target architecture (amd64, arm64, arm, 386)",
						Value:   runtime.GOARCH,
					},
				},
				Action: func(c *cli.Context) error {
					version := c.String("version")
					name := c.String("name")
					output := c.String("output")
					platform := c.String("platform")
					arch := c.String("arch")

					return downloadTask(version, name, output, platform, arch)
				},
			},
			{
				Name:  "set-version",
				Usage: "Set version in a csproj file",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "file",
						Aliases:  []string{"f"},
						Usage:    "Path to csproj file",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "version",
						Aliases:  []string{"v"},
						Usage:    "Version to set",
						Required: true,
					},
				},
				Action: func(c *cli.Context) error {
					file := c.String("file")
					version := c.String("version")
					return setVersion(file, version)
				},
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

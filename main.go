package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

type Config struct {
	AccessToken  string       `yaml:"access_token"`
	Repositories []Repository `yaml:"repositories"`
}

type Repository struct {
	Slug              string `yaml:"slug"`
	CurrentReleaseTag string `yaml:"current_release_tag"`
}

func main() {
	ctx := context.Background()

	config, err := loadConfig("config.yml")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return
	}

	client := createGithubClient(ctx, *config)

	err = checkRepositories(ctx, config, client)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error checking repositories: %v\n", err)
	}

	err = saveConfig("config.yml", config)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		return
	}
}

func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func saveConfig(filename string, config *Config) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func createGithubClient(ctx context.Context, config Config) *github.Client {
	if config.AccessToken != "" {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: config.AccessToken},
		)
		tc := oauth2.NewClient(ctx, ts)
		return github.NewClient(tc)
	}
	return github.NewClient(nil)
}

func parseSlug(slug string) (owner string, repo string, err error) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid slug format: %s", slug)
	}
	return parts[0], parts[1], nil
}

func checkRepositories(ctx context.Context, config *Config, client *github.Client) error {
	for i := range config.Repositories {
		repo := &config.Repositories[i]
		err := checkRepository(ctx, repo, client)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Error checking repository %s: %v\n", repo.Slug, err)
		}
	}
	return nil
}

func checkRepository(ctx context.Context, repo *Repository, client *github.Client) error {
	owner, repoName, err := parseSlug(repo.Slug)
	if err != nil {
		return err
	}

	release, _, err := client.Repositories.GetLatestRelease(ctx, owner, repoName)
	if err != nil {
		return fmt.Errorf("error fetching release for %s: %w", repo.Slug, err)
	}

	tagName := release.GetTagName()
	if repo.CurrentReleaseTag != tagName {
		repo.CurrentReleaseTag = tagName
		fmt.Printf("New release for %s: %s\n", repo.Slug, tagName)
	}

	return nil
}

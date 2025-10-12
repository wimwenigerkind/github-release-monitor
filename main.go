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
	AccessToken string       `yaml:"access_token"`
	Repositorys []Repository `yaml:"repositorys"`
}

type Repository struct {
	Slug              string `yaml:"slug"`
	CurrentReleaseTag string `yaml:"current_release_tag"`
}

func main() {
	configData, err := os.ReadFile("config.yml")
	if err != nil {
		configData = []byte{}
	}

	var config Config
	err = yaml.Unmarshal(configData, &config)
	if err != nil {
		return
	}
	ctx := context.Background()
	var client *github.Client
	if config.AccessToken != "" {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: config.AccessToken},
		)
		tc := oauth2.NewClient(ctx, ts)
		client = github.NewClient(tc)
	} else {
		client = github.NewClient(nil)
	}

	for i := range config.Repositorys {
		repo := &config.Repositorys[i]
		parts := strings.SplitN(repo.Slug, "/", 2)
		if len(parts) != 2 {
			_, _ = fmt.Fprintf(os.Stderr, "Invalid slug format: %s\n", repo.Slug)
			continue
		}

		release, _, err := client.Repositories.GetLatestRelease(ctx, parts[0], parts[1])
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Error fetching release for %s: %v\n", repo.Slug, err)
			continue
		}

		tagName := release.GetTagName()
		if repo.CurrentReleaseTag != tagName {
			repo.CurrentReleaseTag = tagName
			fmt.Printf("New release for %s: %s\n", repo.Slug, tagName)
		}
	}

	marshal, err := yaml.Marshal(config)
	if err != nil {
		return
	}
	err = os.WriteFile("config.yml", marshal, 0644)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		return
	}
}
